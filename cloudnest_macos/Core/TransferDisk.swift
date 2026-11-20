import Foundation
import CryptoKit
import Darwin

actor TransferDisk {
    static let downloadChunkSize = 4 * 1024 * 1024
    private var downloads: [UUID: FileHandle] = [:]
    private var verified: Set<UUID> = []

    func bookmark(_ url: URL) throws -> Data {
        let access = url.startAccessingSecurityScopedResource()
        defer { if access { url.stopAccessingSecurityScopedResource() } }
        return try url.bookmarkData(options: .withSecurityScope, includingResourceValuesForKeys: nil, relativeTo: nil)
    }

    func resolve(_ bookmark: Data) throws -> URL {
        var stale = false
        let url = try URL(resolvingBookmarkData: bookmark, options: [.withSecurityScope, .withoutUI],
                          relativeTo: nil, bookmarkDataIsStale: &stale)
        guard !stale else { throw TransferError.invalidCheckpoint }
        return url
    }

    func prepareUpload(_ url: URL) async throws -> UploadSource {
        let access = url.startAccessingSecurityScopedResource()
        defer { if access { url.stopAccessingSecurityScopedResource() } }
        let handle = try openRegular(url, write: false)
        defer { try? handle.close() }
        let size = try handle.seekToEnd()
        guard size <= 1 << 40 else { throw TransferError.invalidSource }
        let chunkSize = max(4 * 1024 * 1024, Int((size + 16383) / 16384))
        try handle.seek(toOffset: 0)
        var digest = SHA256()
        var hashes: [String] = []
        var read: Int64 = 0
        while true {
            try Task.checkCancellation()
            let data = try readChunk(handle, count: chunkSize)
            if data.isEmpty { break }
            digest.update(data: data)
            hashes.append(Self.hex(SHA256.hash(data: data)))
            read += Int64(data.count)
            await Task.yield()
        }
        guard read == Int64(size) else { throw TransferError.sourceChanged }
        return UploadSource(size: read, chunkSize: chunkSize, chunks: hashes, sha256: Self.hex(digest.finalize()))
    }

    func uploadChunk(_ url: URL, source: UploadSource, index: Int) throws -> Data {
        guard source.chunks.indices.contains(index) else { throw TransferError.invalidCheckpoint }
        let access = url.startAccessingSecurityScopedResource()
        defer { if access { url.stopAccessingSecurityScopedResource() } }
        let handle = try openRegular(url, write: false)
        defer { try? handle.close() }
        guard try handle.seekToEnd() == UInt64(source.size) else { throw TransferError.sourceChanged }
        try handle.seek(toOffset: UInt64(index * source.chunkSize))
        let data = try readChunk(handle, count: source.chunkSize)
        guard Self.hex(SHA256.hash(data: data)) == source.chunks[index] else { throw TransferError.sourceChanged }
        return data
    }

    func openDownload(_ record: TransferRecord, directory: URL) async throws {
        let access = directory.startAccessingSecurityScopedResource()
        defer { if access { directory.stopAccessingSecurityScopedResource() } }
        let url = partURL(record.id, directory: directory)
        let exists = FileManager.default.fileExists(atPath: url.path)
        if !exists && record.confirmedBytes > 0 { throw TransferError.invalidCheckpoint }
        if !exists {
            let descriptor = open(url.path, O_RDWR | O_CREAT | O_EXCL | O_NOFOLLOW, 0o600)
            guard descriptor >= 0 else { throw TransferError.localStorage }
            close(descriptor)
        }
        let handle = try openRegular(url, write: true)
        do {
            guard try handle.seekToEnd() >= UInt64(record.confirmedBytes) else { throw TransferError.invalidCheckpoint }
            try handle.seek(toOffset: 0)
            var remaining = record.confirmedBytes
            var index = 0
            while remaining > 0 {
                try Task.checkCancellation()
                let data = try readChunk(handle, count: min(Self.downloadChunkSize, Int(remaining)))
                guard !data.isEmpty, record.downloadedChunks.indices.contains(index),
                      Self.hex(SHA256.hash(data: data)) == record.downloadedChunks[index] else {
                    throw TransferError.invalidCheckpoint
                }
                remaining -= Int64(data.count)
                index += 1
                await Task.yield()
            }
            guard index == record.downloadedChunks.count else { throw TransferError.invalidCheckpoint }
            try handle.truncate(atOffset: UInt64(record.confirmedBytes))
            downloads[record.id] = handle
        } catch {
            try? handle.close()
            throw error
        }
    }

    func resetDownload(_ id: UUID) throws {
        guard let handle = downloads[id] else { throw TransferError.invalidCheckpoint }
        verified.remove(id)
        try handle.truncate(atOffset: 0)
        try handle.seek(toOffset: 0)
        try handle.synchronize()
    }

    func appendDownload(_ id: UUID, data: Data, offset: Int64) throws {
        guard let handle = downloads[id] else { throw TransferError.invalidCheckpoint }
        verified.remove(id)
        try handle.seek(toOffset: UInt64(offset))
        try handle.write(contentsOf: data)
    }

    func synchronizeDownload(_ id: UUID) throws {
        guard let handle = downloads[id] else { throw TransferError.invalidCheckpoint }
        try handle.synchronize()
    }

    func publishDownload(_ record: TransferRecord, directory: URL) async throws -> String {
        guard let handle = downloads[record.id], let expected = record.sha256 else { throw TransferError.invalidCheckpoint }
        guard try handle.seekToEnd() == UInt64(record.totalBytes) else { throw TransferError.integrity }
        if !verified.contains(record.id) {
            try handle.seek(toOffset: 0)
            var digest = SHA256()
            while true {
                try Task.checkCancellation()
                let data = try readChunk(handle, count: 256 * 1024)
                if data.isEmpty { break }
                digest.update(data: data)
                await Task.yield()
            }
            guard Self.hex(digest.finalize()) == expected else { throw TransferError.integrity }
            verified.insert(record.id)
        }
        try handle.synchronize()
        let source = partURL(record.id, directory: directory)
        guard let name = record.resultName else { throw TransferError.invalidCheckpoint }
        let target = directory.appending(path: name)
        var sourceInfo = stat()
        var targetInfo = stat()
        if lstat(target.path, &targetInfo) == 0 {
            guard fstat(handle.fileDescriptor, &sourceInfo) == 0,
                  sourceInfo.st_dev == targetInfo.st_dev, sourceInfo.st_ino == targetInfo.st_ino else {
                throw TransferError.destinationExists
            }
            return name
        }
        try Task.checkCancellation()
        if link(source.path, target.path) == 0 { return name }
        if errno == EEXIST { throw TransferError.destinationExists }
        throw TransferError.localStorage
    }

    func publicationExists(_ record: TransferRecord, directory: URL) -> Bool {
        guard let name = record.resultName else { return false }
        var part = stat()
        var target = stat()
        return lstat(partURL(record.id, directory: directory).path, &part) == 0 &&
            lstat(directory.appending(path: name).path, &target) == 0 &&
            part.st_mode & S_IFMT == S_IFREG && part.st_dev == target.st_dev && part.st_ino == target.st_ino &&
            part.st_size == record.totalBytes
    }

    func closeDownload(_ id: UUID) {
        verified.remove(id)
        try? downloads.removeValue(forKey: id)?.close()
    }

    func discardDownload(_ id: UUID, directory: URL) throws {
        closeDownload(id)
        let url = partURL(id, directory: directory)
        if FileManager.default.fileExists(atPath: url.path) { try FileManager.default.removeItem(at: url) }
    }

    private func openRegular(_ url: URL, write: Bool) throws -> FileHandle {
        let descriptor = open(url.path, (write ? O_RDWR : O_RDONLY) | O_NOFOLLOW)
        guard descriptor >= 0 else { throw TransferError.localStorage }
        var info = stat()
        guard fstat(descriptor, &info) == 0, info.st_mode & S_IFMT == S_IFREG else {
            close(descriptor)
            throw TransferError.invalidSource
        }
        return FileHandle(fileDescriptor: descriptor, closeOnDealloc: true)
    }

    private func readChunk(_ handle: FileHandle, count: Int) throws -> Data {
        var result = Data()
        while result.count < count {
            guard let data = try handle.read(upToCount: count - result.count), !data.isEmpty else { break }
            result.append(data)
        }
        return result
    }

    private func partURL(_ id: UUID, directory: URL) -> URL {
        directory.appending(path: ".cloudnest-\(id).part")
    }

    nonisolated static func availableName(_ name: String, suffix: Int) -> String {
        guard suffix > 0 else { return name }
        let url = URL(fileURLWithPath: name)
        let ext = url.pathExtension
        let stem = url.deletingPathExtension().lastPathComponent
        return ext.isEmpty ? "\(name) (\(suffix))" : "\(stem) (\(suffix)).\(ext)"
    }

    nonisolated static func hex<S: Sequence>(_ bytes: S) -> String where S.Element == UInt8 {
        bytes.map { String(format: "%02x", $0) }.joined()
    }
}
