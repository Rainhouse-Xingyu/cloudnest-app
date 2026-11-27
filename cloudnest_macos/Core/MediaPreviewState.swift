import Foundation
import Observation

enum MediaPreviewSelection: Equatable, Sendable {
    case photo(FileItem)
    case livePhoto(LivePhotoPair)
}

private struct MediaPreviewResource: DownloadableFile {
    let id: Int64
    let filename: String
    let size: Int64
    let version: Int
    var isDirectory: Bool { false }
}

@MainActor @Observable
final class MediaPreviewState {
    private let session: SessionState
    private let transfers: TransferManager
    private let identity: TransferIdentity?
    private var operation: Task<Void,Never>?
    private var previewIDs: [UUID] = []
    private var generation = UUID()
    private(set) var urls: [URL] = []
    private(set) var busy = false
    private(set) var error: String?
    var receivedBytes: Int64 { previewIDs.reduce(0) { $0 + (transfers.records[$1]?.confirmedBytes ?? 0) } }
    var totalBytes: Int64 { previewIDs.reduce(0) { $0 + (transfers.records[$1]?.totalBytes ?? 0) } }

    init(session: SessionState, transfers: TransferManager) {
        self.session = session; self.transfers = transfers; identity = session.transferIdentity
    }

    func load(_ selection: MediaPreviewSelection) async {
        guard !busy, operation == nil, previewIDs.isEmpty else { return }
        urls = []
        let id = UUID(); generation = id; busy = true; error = nil
        let task = Task {
            defer { if generation == id { busy = false; operation = nil } }
            do {
                try session.requireTransferIdentity(identity)
                let resources: [MediaPreviewResource]
                switch selection {
                case let .photo(file):
                    guard PhotoContract.valid(file) else { throw ServerError.invalidResponse }
                    resources = [MediaPreviewResource(id:file.id,filename:file.filename,size:file.size,version:file.currentVersion)]
                case let .livePhoto(pair):
                    try pair.validate()
                    let current = try await session.livePhoto(id:pair.id)
                    guard current == pair else { throw ServerError.conflict }
                    resources = [pair.image,pair.video].map { MediaPreviewResource(id:$0.id,filename:$0.filename,size:$0.size,version:$0.currentVersion) }
                }
                for file in resources {
                    try Task.checkCancellation(); try session.requireTransferIdentity(identity)
                    let previewID = try await transfers.enqueuePreview(file,version:file.version,expectedIdentity:identity)
                    // close() awaits this task before taking ownership of its IDs.
                    previewIDs.append(previewID)
                }
                while true {
                    try Task.checkCancellation(); try session.requireTransferIdentity(identity)
                    var done = true
                    for previewID in previewIDs {
                        guard let record = transfers.records[previewID] else { throw TransferError.invalidCheckpoint }
                        if [.failed,.cancelled,.paused].contains(record.phase) {
                            error = record.error ?? "预览下载未完成，请关闭后刷新重试。"
                            throw TransferError.invalidDownload
                        }
                        if record.phase != .completed { done = false }
                    }
                    if done { break }
                    try await Task.sleep(for:.milliseconds(50))
                }
                var verified: [URL] = []
                for previewID in previewIDs { verified.append(try await transfers.resultURL(previewID)) }
                if case let .livePhoto(pair) = selection {
                    guard try await session.livePhoto(id:pair.id) == pair else { throw ServerError.conflict }
                }
                try Task.checkCancellation(); try session.requireTransferIdentity(identity)
                guard generation == id else { throw CancellationError() }
                urls = verified
            } catch {
                for previewID in previewIDs { await transfers.dismissPreview(previewID) }
                previewIDs = []
                guard generation == id else { return }
                urls = []
                if !(error is CancellationError), self.error == nil {
                    self.error = (error as? ServerError)?.localizedDescription ?? (error as? TransferError)?.localizedDescription ?? "预览未能完成，请关闭后刷新。"
                }
            }
        }
        operation = task; await task.value
    }

    func close() async {
        generation = UUID(); urls = []
        let task = operation; task?.cancel(); await task?.value
        for id in previewIDs { await transfers.dismissPreview(id) }
        previewIDs = []; operation = nil; busy = false
    }
}
