import Foundation
import Observation

@MainActor @Observable
final class AuditState {
    private let session: SessionState
    private let identity: TransferIdentity?
    private let downloader = AuditCSVDownloader()
    private var operation: Task<Void,Never>?
    private var generation = UUID()
    private(set) var page: AuditPage?
    private(set) var query: AuditQuery?
    private(set) var busy = false
    private(set) var exporting = false
    private(set) var cancelling = false
    private(set) var verifying = false
    private(set) var received: Int64 = 0
    private(set) var totalBytes: Int64 = 0
    private(set) var receipt: AuditExportReceipt?
    private(set) var error: String?

    init(session: SessionState) { self.session = session; identity = session.transferIdentity }

    func load(_ query: AuditQuery, snapshotID: Int64 = 0, beforeID: Int64 = 0, afterID: Int64 = 0) async {
        guard !exporting else { return }
        operation?.cancel()
        let id = UUID(); generation = id
        busy = true; page = nil; receipt = nil; error = nil; self.query = query
        received = 0; totalBytes = 0; verifying = false; cancelling = false
        let task = Task {
            defer { if generation == id { busy = false; operation = nil } }
            do {
                try authorize(query)
                let result = try await session.auditPage(query:query,snapshotID:snapshotID,beforeID:beforeID,afterID:afterID)
                try Task.checkCancellation(); try authorize(query)
                guard generation == id else { return }
                page = result
            } catch where Task.isCancelled {
                if generation == id { self.error = "读取已取消。" }
            } catch is CancellationError { return }
            catch {
                guard generation == id else { return }
                self.error = (error as? ServerError)?.localizedDescription ?? "审计记录读取失败，请检查连接。"
            }
        }
        operation = task; await task.value
    }

    func next() async {
        guard !busy, let page, page.nextBeforeID > 0 else { return }
        await load(page.query,snapshotID:page.snapshotID,beforeID:page.nextBeforeID)
    }

    func previous() async {
        guard !busy, let page, page.nextAfterID > 0 else { return }
        await load(page.query,snapshotID:page.snapshotID,afterID:page.nextAfterID)
    }

    var exportSelection: AuditExportSelection? {
        guard let page, page.snapshotID > 0, page.total > 0 else { return nil }
        return AuditExportSelection(query:page.query,snapshotID:page.snapshotID,total:page.total)
    }

    func export(_ selection: AuditExportSelection, to destination: URL) async {
        guard !busy else { return }
        guard selection == exportSelection else { error = "查询范围已变化，请重新选择导出位置。"; return }
        let id = UUID(); generation = id
        busy = true; exporting = true; cancelling = false; verifying = false; received = 0; totalBytes = 0; receipt = nil; error = nil
        let task = Task {
            defer { if generation == id { busy = false; exporting = false; cancelling = false; verifying = false; operation = nil } }
            do {
                try authorize(selection.query)
                let source = try await session.auditExportResponse(selection:selection,expectedIdentity:identity)
                defer { source.stream.cancel() }
                let result = try await downloader.download(source,destination:destination,beforePublish: {
                    try await self.authorizePublication(id,query:selection.query)
                },progress: { received,total,verifying in
                    await self.update(id,received:received,total:total,verifying:verifying)
                })
                guard generation == id else { return }
                receipt = result
                if result.cleanupIncomplete { error = "CSV 已验证并保存，但临时副本未能清理，请检查所选目录。" }
            } catch is CancellationError {
                if generation == id { error = "导出已取消，未保存新的 CSV。" }
            } catch {
                guard generation == id else { return }
                let cause = (error as? AuditExportFailure)?.cause ?? error
                if let failure = cause as? ServerError, [.forbidden,.unauthorized,.sessionChanged].contains(failure) { page = nil }
                self.error = (error as? AuditExportFailure)?.localizedDescription ?? (error as? ServerError)?.localizedDescription
                    ?? (error as? TransferError)?.localizedDescription ?? "导出未完成，请检查连接与本地空间。"
            }
        }
        operation = task; await task.value
    }

    private func authorize(_ query: AuditQuery) throws {
        try session.requireTransferIdentity(identity)
        guard !query.all || session.account?.isAdmin == true else { throw ServerError.forbidden }
    }
    private func authorizePublication(_ id: UUID, query: AuditQuery) throws {
        guard generation == id else { throw CancellationError() }
        try authorize(query)
    }
    private func update(_ id: UUID, received: Int64, total: Int64, verifying: Bool) {
        guard generation == id else { return }
        self.received = received; totalBytes = total; self.verifying = verifying
    }
    func cancel() {
        guard busy else { return }
        if exporting { cancelling = true }
        operation?.cancel()
    }
    func discard() {
        generation = UUID(); operation?.cancel(); operation = nil
        page = nil; query = nil; receipt = nil; error = nil; busy = false; exporting = false; cancelling = false; verifying = false
        received = 0; totalBytes = 0
    }
}
