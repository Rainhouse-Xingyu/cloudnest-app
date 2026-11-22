import Foundation
import Observation

@MainActor @Observable
final class FRPConfigState {
    private(set) var preview: String?
    private(set) var selection: FRPConfigSelection?
    private(set) var busy = false
    private(set) var submitted = false
    private(set) var saved: FRPConfig?
    private(set) var error: String?
    private let session: SessionState
    private let identity: TransferIdentity?
    private var revision = UUID()
    private var request: Task<String, any Error>?

    init(session: SessionState) { self.session = session; identity = session.transferIdentity }

    func clearPreview() {
        revision = UUID()
        request?.cancel()
        request = nil
        preview = nil
        selection = nil
        // Saving is deliberately not cancelled or made repeatable by changing
        // the preview. An absent receipt cannot prove the append did not commit.
        if !submitted { busy = false; error = nil }
    }

    func loadPreview(_ selection: FRPConfigSelection) async {
        guard !submitted else { return }
        clearPreview()
        let revision = self.revision
        busy = true
        let task = Task { try await session.previewFRPConfig(selection, expectedIdentity: identity) }
        request = task
        defer { if self.revision == revision { busy = false; request = nil } }
        do {
            let value = try await withTaskCancellationHandler { try await task.value } onCancel: { task.cancel() }
            try Task.checkCancellation()
            guard self.revision == revision else { return }
            try session.requireTransferIdentity(identity)
            self.selection = selection
            preview = value
        } catch {
            guard self.revision == revision, !task.isCancelled, !(error is CancellationError) else { return }
            self.error = message(error)
        }
    }

    func save(expectedSelection: FRPConfigSelection) async {
        guard !busy, !submitted, selection == expectedSelection, preview != nil else { return }
        do { try session.requireTransferIdentity(identity) }
        catch { self.error = message(error); return }
        busy = true
        submitted = true
        error = nil
        defer { busy = false }
        do {
            let result = try await session.saveFRPConfig(expectedSelection, expectedIdentity: identity)
            try session.requireTransferIdentity(identity)
            saved = result
            preview = nil
        } catch {
            self.error = "未取得保存成功回执。\(message(error))请关闭并刷新配置历史核对，避免重复保存。"
        }
    }

    private func message(_ error: any Error) -> String {
        if error as? ServerError == .conflict { return "服务器版本或映射策略发生变化，请重新读取后核对。" }
        return (error as? ServerError)?.localizedDescription ?? "无法完成请求，请检查连接。"
    }
}
