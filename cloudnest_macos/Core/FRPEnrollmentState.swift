import Foundation
import Observation

enum FRPCredentialOperation: String, Identifiable, Sendable {
    case issue, revoke
    var id: String { rawValue }
}

@MainActor @Observable
final class FRPEnrollmentState {
    private let session: SessionState
    private let identity: TransferIdentity?
    private let deviceID: Int64
    let operation: FRPCredentialOperation
    private var closed = false
    private(set) var busy = false
    private(set) var submitted = false
    private(set) var receipt: FRPEnrollment?
    private(set) var revoked = false
    private(set) var error: String?

    init(session: SessionState, deviceID: Int64, operation: FRPCredentialOperation) {
        self.session = session; identity = session.transferIdentity
        self.deviceID = deviceID; self.operation = operation
    }

    func submit(password: String) async {
        guard !closed, !busy, !submitted else { return }
        error = nil
        do { try session.requireTransferIdentity(identity) }
        catch { self.error = (error as? ServerError)?.localizedDescription; return }
        guard !password.isEmpty, password.utf8.count <= 72 else { error = "请输入当前密码（最多 72 个 UTF-8 字节）。"; return }
        busy = true; submitted = true
        defer { busy = false }
        do {
            switch operation {
            case .issue:
                let result = try await session.issueFRPEnrollment(deviceID: deviceID, password: password, expectedIdentity: identity)
                try Task.checkCancellation()
                guard !closed else { return }
                receipt = result
            case .revoke:
                try await session.revokeFRPCredential(deviceID: deviceID, password: password, expectedIdentity: identity)
                try Task.checkCancellation()
                guard !closed else { return }
                revoked = true
            }
        } catch {
            guard !closed else { return }
            if let rejection = error as? ServerError, [.reauthenticationRejected, .forbidden, .rateLimited, .invalidOperation, .notFound].contains(rejection) {
                submitted = false
                self.error = rejection == .notFound ? "设备不可用，请关闭并刷新核对。" : rejection.localizedDescription
            } else {
                self.error = operation == .issue
                    ? "未取得注册码回执，请关闭后核对设备；需要时重新确认申请，新码会替换旧的待兑换码。"
                    : "未取得撤销回执，请关闭后核对；不要据此认定隧道已停止。"
            }
        }
    }

    func expire() { receipt = nil; error = "注册码已过期，请关闭后重新确认申请。" }
    func close() { closed = true; receipt = nil }
}
