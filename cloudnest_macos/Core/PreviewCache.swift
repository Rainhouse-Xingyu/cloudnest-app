import Foundation
import Darwin

// Temporary Quick Look copies have one owner per request. A reservation remains
// pinned until that view dismisses it; there is no eviction of an open preview.
actor PreviewCache {
    struct Limits: Sendable {
        var bytes: Int64 = 512 * 1024 * 1024
        var fileBytes: Int64 = 256 * 1024 * 1024
        var entries = 16
    }

    private let root: URL
    private let limits: Limits
    private var prepared = false
    private var reservations: [UUID: Int64] = [:]
    private var cleanupPending: Set<UUID> = []
    private static let marker = Data("CloudNest temporary previews v1\n".utf8)

    init(directory: URL, limits: Limits = Limits()) {
        root = directory
        self.limits = limits
    }

    // Called only while TransferStore owns the process lock. UUID directories in
    // this marked namespace are disposable, including a crash before journaling.
    func recover() throws {
        guard !prepared else { return }
        guard limits.bytes > 0, limits.fileBytes > 0, limits.entries > 0 else { throw TransferError.previewCapacity }
        let fm = FileManager.default
        if !fm.fileExists(atPath: root.path) {
            try fm.createDirectory(at: root, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        }
        guard isDirectory(root) else { throw TransferError.previewStorage }
        let marker = root.appending(path: ".format-v1")
        var children = try fm.contentsOfDirectory(at: root, includingPropertiesForKeys: nil)
        if children.isEmpty {
            try Self.marker.write(to: marker, options: .atomic)
            try fm.setAttributes([.posixPermissions: 0o600], ofItemAtPath: marker.path)
            children = [marker]
        }
        var info = stat()
        guard lstat(marker.path, &info) == 0, info.st_mode & S_IFMT == S_IFREG,
              info.st_size == Self.marker.count, try Data(contentsOf: marker) == Self.marker else {
            throw TransferError.previewStorage
        }
        // Validate the complete inventory before removing anything. Unknown
        // entries and symlinked directories are retained and stop admission.
        let entries = children.filter { $0.lastPathComponent != ".format-v1" }
        guard entries.allSatisfy({ UUID(uuidString: $0.lastPathComponent) != nil && isDirectory($0) }) else {
            throw TransferError.previewStorage
        }
        for entry in entries { try fm.removeItem(at: entry) }
        prepared = true
    }

    func reserve(_ id: UUID, bytes: Int64) throws -> URL {
        try recover()
        for stale in Array(cleanupPending) { try remove(stale) }
        guard bytes >= 0, bytes <= limits.fileBytes else { throw TransferError.previewTooLarge }
        let used = reservations.values.reduce(Int64(0), +)
        guard reservations[id] == nil, reservations.count < limits.entries,
              used <= limits.bytes, bytes <= limits.bytes - used else { throw TransferError.previewCapacity }
        let folder = root.appending(path: id.uuidString, directoryHint: .isDirectory)
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: false,
                                                attributes: [.posixPermissions: 0o700])
        reservations[id] = bytes
        return folder
    }

    func validate(_ id: UUID, total: Int64) throws {
        guard let reserved = reservations[id], total >= 0, total <= reserved else { throw TransferError.previewTooLarge }
    }

    func remove(_ id: UUID) throws {
        guard reservations[id] != nil else { return }
        cleanupPending.insert(id)
        let folder = root.appending(path: id.uuidString, directoryHint: .isDirectory)
        // Never follow a replacement symlink or delete an unowned destination.
        guard isDirectory(root) else { throw TransferError.previewStorage }
        var info = stat()
        if lstat(folder.path, &info) == 0 {
            guard info.st_mode & S_IFMT == S_IFDIR else { throw TransferError.previewStorage }
            try FileManager.default.removeItem(at: folder)
        } else if errno != ENOENT { throw TransferError.previewStorage }
        reservations[id] = nil
        cleanupPending.remove(id)
    }

    private func isDirectory(_ url: URL) -> Bool {
        var info = stat()
        return lstat(url.path, &info) == 0 && info.st_mode & S_IFMT == S_IFDIR
    }
}
