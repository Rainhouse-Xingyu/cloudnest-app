import AppKit
import SwiftUI

struct FRPEnrollmentView: View {
    let session: SessionState
    let device: FRPDevice
    private let identity: TransferIdentity?
    @Environment(\.dismiss) private var dismiss
    @State private var state: FRPEnrollmentState
    @State private var password = ""
    @State private var confirming = false
    @State private var requestTask: Task<Void, Never>?
    @State private var copiedChangeCount: Int?
    @State private var copied = false

    init(session: SessionState, device: FRPDevice, operation: FRPCredentialOperation) {
        self.session = session; self.device = device; identity = session.transferIdentity
        _state = State(initialValue: FRPEnrollmentState(session: session, deviceID: device.id, operation: operation))
    }

    private var issuing: Bool { state.operation == .issue }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text(issuing ? "申请 Agent 注册码" : "撤销设备凭据").font(.title2)
            Text("\(device.name) · 设备 \(String(device.id))").foregroundStyle(.secondary)
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    if let receipt = state.receipt {
                        Text("注册码已生成，仅供此设备一次兑换。")
                        LabeledContent("到期时间", value: receipt.expiresAt.formatted(date: .abbreviated, time: .standard))
                        SecureField("注册码", text: .constant(receipt.code)).disabled(true).privacySensitive()
                        Button(copied ? "再次复制注册码" : "复制注册码") { copy(receipt) }
                        Text("在目标电脑的 CloudNest Agent 注册提示中输入此码，不要放入命令参数。关闭本窗口或到期后清除本窗口保存的码；若剪贴板仍是本次复制内容，也会清除。其他应用已读取的副本无法收回。")
                            .font(.callout).foregroundStyle(.secondary)
                    } else if state.revoked {
                        Text("服务器已确认撤销当前设备凭据和待兑换注册码。")
                        Text("Agent 后续认证将被拒绝。离线或失联设备的现有隧道是否停止，仍需核对实际进程和端口；历史配置与资源占用保留。")
                            .font(.callout).foregroundStyle(.secondary)
                    } else {
                        Text(issuing
                            ? "注册码有效十分钟，新申请替换旧的待兑换码。现有 Agent 凭据在新码兑换成功后失效；首次申请也会停用该设备旧的用户 JWT 执行入口。"
                            : "撤销当前 Agent 凭据及所有待兑换注册码。继续使用此设备需重新申请并兑换；撤销不会删除历史或证明现有隧道已停止。")
                            .font(.callout).foregroundStyle(.secondary)
                        SecureField("当前账户密码", text: $password).textContentType(.password).privacySensitive()
                            .disabled(state.busy || state.submitted)
                        Text("密码只用于本次服务器验证，不保存。关闭窗口不会撤销已送出的请求。")
                            .font(.callout).foregroundStyle(.secondary)
                    }
                    if let error = state.error { Text(error).foregroundStyle(.secondary) }
                }.frame(maxWidth: .infinity, alignment: .leading)
            }.frame(minHeight: 80, maxHeight: .infinity).layoutPriority(-1)
            HStack {
                if state.busy { ProgressView().controlSize(.small) }
                Spacer()
                Button("关闭") { dismiss() }.keyboardShortcut(.cancelAction)
                if state.receipt == nil && !state.revoked {
                    Button(issuing ? "申请注册码…" : "撤销凭据…", role: issuing ? nil : .destructive) { confirming = true }
                        .disabled(state.busy || state.submitted || password.isEmpty)
                }
            }.fixedSize(horizontal: false, vertical: true)
        }
        .padding(24).frame(width: 520).frame(minHeight: 360, idealHeight: 460)
        .confirmationDialog(issuing ? "申请 Agent 注册码" : "撤销设备凭据", isPresented: $confirming, titleVisibility: .visible) {
            Button(issuing ? "确认申请" : "确认撤销", role: issuing ? nil : .destructive) {
                let currentPassword = password
                password = ""
                requestTask = Task { await state.submit(password: currentPassword) }
            }
        } message: {
            Text(issuing
                ? "设备：\(device.name)（\(String(device.id))）。申请替换待兑换码，兑换会使当前 Agent 凭据失效。请确认将此码交给预期的目标电脑。"
                : "设备：\(device.name)（\(String(device.id))）。撤销当前 Agent 凭据和待兑换码，不自动释放运行占用。")
        }
        .task(id: state.receipt?.expiresAt) {
            guard let expiry = state.receipt?.expiresAt else { return }
            do {
                try await Task.sleep(for: .seconds(max(0, expiry.timeIntervalSinceNow)))
                clearCopiedCode(); state.expire()
            } catch { /* Disappearance or replacement cancels expiration only. */ }
        }
        .onChange(of: session.transferIdentity) { _, current in
            if current != identity { password = ""; state.close(); clearCopiedCode(); dismiss() }
        }
        .onDisappear { password = ""; requestTask?.cancel(); state.close(); clearCopiedCode() }
    }

    private func copy(_ receipt: FRPEnrollment) {
        guard receipt.expiresAt > Date(), session.transferIdentity == identity else { clearCopiedCode(); state.expire(); return }
        let board = NSPasteboard.general
        board.clearContents()
        if board.setString(receipt.code, forType: .string) { copiedChangeCount = board.changeCount; copied = true }
    }

    private func clearCopiedCode() {
        if let count = copiedChangeCount, NSPasteboard.general.changeCount == count { NSPasteboard.general.clearContents() }
        copiedChangeCount = nil; copied = false
    }
}
