import Foundation
import Darwin

// All filesystem operations are relative to the locked directory descriptor.
// A path replacement cannot redirect an in-flight command journal write.
actor FRPCommandStore {
    nonisolated static var defaultDirectory: URL {
        FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appending(path: "org.cloudnest.macos/frp-commands", directoryHint: .isDirectory)
    }
    private static let maximumEntries = 1024
    private static let maximumBytes = 16 * 1024
    private var directoryFD: Int32 = -1
    private var lockFD: Int32 = -1
    private var loaded = false
    private var poisoned = false
    private var entryCount = 0
    private var records: [UUID: FRPCommandIntent] = [:]

    init(directory: URL) throws {
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true,
                                                attributes: [.posixPermissions: 0o700])
        let root = Darwin.open(directory.path, O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC)
        guard root >= 0 else { throw FRPCommandStoreError.storage }
        let lock = openat(root, ".lock", O_CREAT | O_RDWR | O_NOFOLLOW | O_CLOEXEC, 0o600)
        var info = stat()
        guard lock >= 0, fstat(lock, &info) == 0, info.st_mode & S_IFMT == S_IFREG, info.st_nlink == 1 else {
            if lock >= 0 { Darwin.close(lock) }
            Darwin.close(root)
            throw FRPCommandStoreError.storage
        }
        guard flock(lock, LOCK_EX | LOCK_NB) == 0 else {
            Darwin.close(lock)
            Darwin.close(root)
            throw FRPCommandStoreError.alreadyRunning
        }
        directoryFD = root
        lockFD = lock
    }

    deinit {
        if lockFD >= 0 { Darwin.close(lockFD) }
        if directoryFD >= 0 { Darwin.close(directoryFD) }
    }

    func close() {
        if lockFD >= 0 { Darwin.close(lockFD); lockFD = -1 }
        if directoryFD >= 0 { Darwin.close(directoryFD); directoryFD = -1 }
        records = [:]
    }

    func load() throws -> [FRPCommandIntent] {
        try ensureLoaded()
        return records.values.sorted { $0.createdAt < $1.createdAt }
    }

    func insert(_ record: FRPCommandIntent) throws {
        try ensureLoaded()
        try record.validate()
        guard records[record.id] == nil, record.revision == 0, record.outcome == .unconfirmed else {
            throw FRPCommandStoreError.conflict
        }
        guard entryCount < Self.maximumEntries else { throw FRPCommandStoreError.full }
        try write(record)
    }

    func replace(_ record: FRPCommandIntent) throws {
        try ensureLoaded()
        try record.validate()
        guard let previous = records[record.id], previous.sameOperation(as: record),
              record.revision == previous.revision + 1,
              previous.outcome != .accepted || (record.outcome == .accepted && record.commandID == previous.commandID) else {
            throw FRPCommandStoreError.conflict
        }
        try write(record)
    }

    private func ensureOpen() throws {
        guard directoryFD >= 0 else { throw FRPCommandStoreError.closed }
        guard !poisoned else { throw FRPCommandStoreError.storage }
    }

    private func ensureLoaded() throws {
        try ensureOpen()
        guard !loaded else { return }
        let copy = dup(directoryFD)
        guard copy >= 0 else { throw FRPCommandStoreError.storage }
        guard let directory = fdopendir(copy) else { Darwin.close(copy); throw FRPCommandStoreError.storage }
        defer { closedir(directory) }
        var result: [UUID: FRPCommandIntent] = [:]
        var entries = 0
        // dup shares the enumeration offset with directoryFD; rewind on every
        // initial load, including after a failed validation.
        rewinddir(directory)
        while true {
            errno = 0
            guard let entry = readdir(directory) else {
                guard errno == 0 else { throw FRPCommandStoreError.storage }
                break
            }
            let name = withUnsafePointer(to: &entry.pointee.d_name) {
                $0.withMemoryRebound(to: CChar.self, capacity: Int(entry.pointee.d_namlen) + 1) { String(cString: $0) }
            }
            if name == "." || name == ".." || name == ".lock" { continue }
            entries += 1
            guard entries <= Self.maximumEntries else { throw FRPCommandStoreError.full }
            if name.hasPrefix("."), name.hasSuffix(".tmp"), UUID(uuidString: String(name.dropFirst().dropLast(4))) != nil {
                // A crash may leave our private temporary file. Preserve it;
                // never infer a remote request from an uncommitted temp file.
                continue
            }
            guard name.hasSuffix(".json"), let id = UUID(uuidString: String(name.dropLast(5))),
                  name == "\(id).json" else { throw FRPCommandStoreError.invalidJournal }
            let file = openat(directoryFD, name, O_RDONLY | O_NOFOLLOW | O_CLOEXEC)
            guard file >= 0 else { throw FRPCommandStoreError.invalidJournal }
            let handle = FileHandle(fileDescriptor: file, closeOnDealloc: true)
            defer { try? handle.close() }
            var info = stat()
            guard fstat(file, &info) == 0, info.st_mode & S_IFMT == S_IFREG, info.st_nlink == 1,
                  info.st_size > 0, info.st_size <= Self.maximumBytes else { throw FRPCommandStoreError.invalidJournal }
            let data = try handle.read(upToCount: Self.maximumBytes + 1) ?? Data()
            guard data.count <= Self.maximumBytes else { throw FRPCommandStoreError.invalidJournal }
            let record: FRPCommandIntent
            do { record = try JSONDecoder().decode(FRPCommandIntent.self, from: data) }
            catch { throw FRPCommandStoreError.invalidJournal }
            try record.validate()
            guard record.id == id else { throw FRPCommandStoreError.invalidJournal }
            result[id] = record
        }
        records = result
        entryCount = entries
        loaded = true
    }

    private func write(_ record: FRPCommandIntent) throws {
        let data = try JSONEncoder().encode(record)
        guard data.count <= Self.maximumBytes else { throw FRPCommandStoreError.invalidJournal }
        let temporary = ".\(UUID()).tmp"
        let file = openat(directoryFD, temporary, O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW | O_CLOEXEC, 0o600)
        guard file >= 0 else { throw FRPCommandStoreError.storage }
        defer { Darwin.close(file); unlinkat(directoryFD, temporary, 0) }
        try data.withUnsafeBytes { bytes in
            var offset = 0
            while offset < bytes.count {
                let count = Darwin.write(file, bytes.baseAddress!.advanced(by: offset), bytes.count - offset)
                if count < 0 && errno == EINTR { continue }
                guard count > 0 else { throw FRPCommandStoreError.storage }
                offset += count
            }
        }
        guard fsync(file) == 0 else { throw FRPCommandStoreError.storage }
        guard renameat(directoryFD, temporary, directoryFD, "\(record.id).json") == 0 else { throw FRPCommandStoreError.storage }
        // If the directory sync fails, the result is uncertain. Preserve the
        // file and disable further submissions until a fresh store reload.
        guard fsync(directoryFD) == 0 else { poisoned = true; throw FRPCommandStoreError.storage }
        if records[record.id] == nil { entryCount += 1 }
        records[record.id] = record
    }
}
