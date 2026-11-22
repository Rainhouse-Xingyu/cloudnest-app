import Foundation
import Observation

@MainActor @Observable
final class FRPServerState {
    private(set) var items: [FRPServer] = []
    private(set) var busy = false
    private(set) var loaded = false
    private(set) var error: String?
    private(set) var afterID: Int64 = 0
    private(set) var nextAfterID: Int64 = 0
    private let session: SessionState
    private let identity: TransferIdentity?
    private var request: Task<FRPServerPage, any Error>?
    private var revision = UUID()

    init(session: SessionState) { self.session = session; identity = session.transferIdentity }

    func cancel() { revision = UUID(); request?.cancel(); request = nil; busy = false }

    func load(afterID: Int64 = 0) async {
        cancel()
        let revision = self.revision
        busy = true
        error = nil
        items = []
        nextAfterID = 0
        let task = Task { try session.requireTransferIdentity(identity); return try await session.frpServers(afterID: afterID) }
        request = task
        defer { if self.revision == revision { busy = false; request = nil; loaded = true } }
        do {
            let page = try await withTaskCancellationHandler { try await task.value } onCancel: { task.cancel() }
            try Task.checkCancellation()
            guard self.revision == revision else { return }
            try session.requireTransferIdentity(identity)
            items = page.items
            self.afterID = afterID
            nextAfterID = page.nextAfterID
        } catch {
            guard self.revision == revision, !task.isCancelled, !(error is CancellationError) else { return }
            self.error = (error as? ServerError)?.localizedDescription ?? "无法读取服务器，请检查连接后重试。"
        }
    }
}
