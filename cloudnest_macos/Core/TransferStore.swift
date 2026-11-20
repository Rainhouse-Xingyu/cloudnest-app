import Foundation
import Darwin

actor TransferStore {
    nonisolated static var defaultDirectory: URL {
        FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appending(path: "org.cloudnest.macos/transfers", directoryHint: .isDirectory)
    }

    private let directory: URL
    private let lock: Int32
    private var revisions: [UUID: Int64] = [:]

    init(directory: URL) throws {
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true,
                                                attributes: [.posixPermissions: 0o700])
        let descriptor = open(directory.appending(path: ".lock").path, O_CREAT | O_RDWR | O_NOFOLLOW, 0o600)
        guard descriptor >= 0 else { throw TransferError.localStorage }
        guard flock(descriptor, LOCK_EX | LOCK_NB) == 0 else {
            close(descriptor)
            throw TransferError.alreadyRunning
        }
        self.directory = directory
        lock = descriptor
    }

    deinit { close(lock) }

    func load() throws -> [TransferRecord] {
        let urls = try FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil)
        return try urls.filter { $0.pathExtension == "json" }.map { url in
            guard let id = UUID(uuidString: url.deletingPathExtension().lastPathComponent) else {
                throw TransferError.invalidCheckpoint
            }
            let record = try JSONDecoder().decode(TransferRecord.self, from: Data(contentsOf: url))
            guard record.id == id, record.previewID == nil, record.totalBytes >= 0, record.confirmedBytes >= 0,
                  record.confirmedBytes <= record.totalBytes, record.epoch >= 0,
                  record.revision >= 0, record.revision < Int64.max,
                  record.identity.userID > 0, record.parentID >= 0,
                  !record.name.isEmpty, record.name != ".", record.name != "..",
                  URL(fileURLWithPath: record.name).lastPathComponent == record.name,
                  record.resultName.map({ URL(fileURLWithPath: $0).lastPathComponent == $0 && $0 != "." && $0 != ".." }) ?? true else {
                throw TransferError.invalidCheckpoint
            }
            _ = try ServerOrigin(record.identity.server)
            if let source = record.source {
                guard source.size >= 0, source.size <= 1 << 40, source.size == record.totalBytes,
                      source.chunkSize > 0, source.chunkSize <= 64 * 1024 * 1024,
                      source.chunks.count <= 16384,
                      Int64(source.chunks.count) == (source.size == 0 ? 0 : 1 + (source.size - 1) / Int64(source.chunkSize)),
                      source.chunks.allSatisfy(Self.validHash), Self.validHash(source.sha256) else {
                    throw TransferError.invalidCheckpoint
                }
            }
            let confirmedChunks = record.confirmedBytes == 0 ? 0 : 1 + (record.confirmedBytes - 1) / Int64(TransferDisk.downloadChunkSize)
            if record.direction.isDownload {
                guard record.fileID.map({ $0 > 0 }) ?? false,
                      Int64(record.downloadedChunks.count) == confirmedChunks,
                      record.downloadedChunks.allSatisfy(Self.validHash) else { throw TransferError.invalidCheckpoint }
            }
            if let version = record.fileVersion {
                guard version > 0, record.direction == .versionDownload, !record.isArchive,
                      record.versionSize.map({ $0 >= 0 && $0 == record.totalBytes }) ?? true else { throw TransferError.invalidCheckpoint }
            } else if record.versionSize != nil || record.direction == .versionDownload { throw TransferError.invalidCheckpoint }
            revisions[id] = record.revision
            return record
        }
    }

    func save(_ record: TransferRecord) throws {
        guard record.revision >= (revisions[record.id] ?? 0) else { return }
        let url = directory.appending(path: "\(record.id).json")
        try JSONEncoder().encode(record).write(to: url, options: .atomic)
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: url.path)
        revisions[record.id] = record.revision
    }
    private static func validHash(_ value: String) -> Bool {
        value.utf8.count == 64 && value.utf8.allSatisfy { (48...57).contains($0) || (97...102).contains($0) }
    }

}
