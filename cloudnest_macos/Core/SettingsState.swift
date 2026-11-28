import Foundation
import Observation

@MainActor @Observable
final class SettingsState {
    private let session: SessionState
    let identity: TransferIdentity?
    private var operation: Task<Void, Never>?
    private var generation = UUID()
    private(set) var system: SystemSettingsSnapshot?
    private(set) var window: UserRateWindow?
    private(set) var busy = false
    private(set) var error: String?

    init(session: SessionState) { self.session = session; identity = session.transferIdentity }

    func load() async {
        operation?.cancel()
        let id = UUID(); generation = id
        busy = true; error = nil; system = nil; window = nil
        let task = Task {
            defer { if generation == id { busy = false; operation = nil } }
            do {
                try session.requireTransferIdentity(identity)
                async let settings = session.systemSettings()
                async let personal = session.rateWindow()
                let result = try await (settings, personal)
                try Task.checkCancellation()
                try session.requireTransferIdentity(identity)
                guard generation == id else { return }
                system = result.0; window = result.1
            } catch is CancellationError { return }
            catch {
                guard generation == id else { return }
                self.error = (error as? ServerError)?.localizedDescription ?? "设置读取失败，请检查连接。"
            }
        }
        operation = task; await task.value
    }

    func cancel() { generation = UUID(); operation?.cancel(); operation = nil; busy = false; system = nil; window = nil }
}

@MainActor @Observable
final class SettingsEditState {
    private(set) var busy = false
    private(set) var locked = false
    private(set) var saved = false
    private(set) var error: String?

    func save(_ action: @MainActor () async throws -> Void) async {
        guard !busy, !locked, !saved else { return }
        busy = true; error = nil
        defer { busy = false }
        do { try await action(); saved = true }
        catch {
            locked = (error as? ServerError) != .invalidOperation
            switch error as? ServerError {
            case .conflict: self.error = "设置已被其他客户端修改。请关闭并刷新，再核对后保存。"
            case .invalidOperation: self.error = "参数未被接受，请检查输入。"
            case .forbidden, .unauthorized, .sessionChanged: self.error = (error as? ServerError)?.localizedDescription
            default: self.error = "未取得保存成功回执。请关闭并刷新核对，避免重复提交。"
            }
        }
    }
}
