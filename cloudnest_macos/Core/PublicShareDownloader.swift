import Foundation

struct PublicShareDownloadReceipt: Sendable {
    let url: URL
    let bytes: Int64
    let sha256: String
    let cleanupIncomplete: Bool
}

struct PublicShareTransferFailure: LocalizedError {
    let cause: any Error
    let cleanupIncomplete: Bool
    var errorDescription: String? {
        let message = (cause as? PublicShareError)?.localizedDescription
            ?? (cause as? TransferError)?.localizedDescription
            ?? (cause is CancellationError ? "下载已取消。" : "下载未能完成，请检查连接与本地空间。")
        return message + (cleanupIncomplete ? "临时文件未能清理，请检查所选目录的访问权限。" : "")
    }
}

struct PublicShareDownloader: Sendable {
    private let disk = TransferDisk()
    private let legacyPreviews: LegacyPreviewDirectory

    init(legacyPreviews: LegacyPreviewDirectory = LegacyPreviewDirectory(store: TransferStore.defaultDirectory)) {
        self.legacyPreviews = legacyPreviews
    }

    func download(client: PublicShareClient, metadata: PublicShareMetadata, passcode: String, destination: URL,
                  progress: @escaping @Sendable (Int64, Int64, Bool) async -> Void) async throws -> PublicShareDownloadReceipt {
        let id = UUID(), directory = destination.deletingLastPathComponent(), name = destination.lastPathComponent
        guard destination.isFileURL, !name.isEmpty, name != ".", name != "..", !name.contains("/") else { throw TransferError.invalidSource }
        guard !legacyPreviews.contains(directory) else { throw TransferError.legacyPreview }
        let access = directory.startAccessingSecurityScopedResource()
        defer { if access { directory.stopAccessingSecurityScopedResource() } }
        try Task.checkCancellation()
        try await disk.openTransientDownload(id, directory: directory)
        do {
            let (stream, response) = try await client.downloadResponse(metadata.target, passcode: passcode)
            defer { stream.cancel() }
            return try await withTaskCancellationHandler {
                let identity = try DownloadIdentity(response: response)
                guard identity.length <= 1 << 40,
                      metadata.isDirectory ? identity.length >= 22 : identity.length == metadata.size else { throw TransferError.invalidDownload }
                var received: Int64 = 0
                for try await chunk in stream.chunks {
                    defer { chunk.acknowledge() }
                    try Task.checkCancellation()
                    guard chunk.data.count <= TransferDisk.downloadChunkSize,
                          Int64(chunk.data.count) <= identity.length - received else { throw TransferError.invalidDownload }
                    try await disk.appendDownload(id, data: chunk.data, offset: received)
                    received += Int64(chunk.data.count)
                    await progress(received, identity.length, false)
                }
                try Task.checkCancellation()
                guard received == identity.length else { throw TransferError.integrity }
                await progress(received, identity.length, true)
                var published: String?
                for suffix in 0...1000 {
                    try Task.checkCancellation()
                    do {
                        published = try await disk.publishVerifiedDownload(id, totalBytes: identity.length, sha256: identity.sha256,
                            name: TransferDisk.availableName(name, suffix: suffix), directory: directory)
                        break
                    } catch TransferError.destinationExists { continue }
                }
                guard let published else { throw TransferError.destinationExists }
                // Publication wins a concurrent cancellation. Do not report an
                // already saved, verified file as cancelled and invite a replay.
                var cleanupIncomplete = false
                do { try await disk.discardDownload(id, directory: directory) }
                catch { cleanupIncomplete = true }
                return PublicShareDownloadReceipt(url: directory.appending(path: published), bytes: identity.length,
                    sha256: identity.sha256, cleanupIncomplete: cleanupIncomplete)
            } onCancel: { stream.cancel() }
        } catch {
            var cleanupIncomplete = false
            do { try await disk.discardDownload(id, directory: directory) }
            catch { cleanupIncomplete = true }
            if error is CancellationError && !cleanupIncomplete { throw CancellationError() }
            throw PublicShareTransferFailure(cause: error, cleanupIncomplete: cleanupIncomplete)
        }
    }
}
