import SwiftUI
import AppKit

struct PublicShareView: View {
    @Environment(\.dismiss) private var dismiss
    @State private var state: PublicShareState
    @State private var link = ""
    @State private var passcode = ""
    @State private var pendingRead: PublicShareTarget?
    @State private var pendingDownload: Selection?
    @State private var inputError: String?
    private struct Selection { let metadata: PublicShareMetadata; let destination: URL }

    init(client: PublicShareClient) { _state = State(initialValue: PublicShareState(client: client)) }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("打开分享链接").font(.title2)
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    Text("无需登录。请核对链接服务器，再决定是否发送提取码。")
                        .foregroundStyle(.secondary)
                    SecureField("HTTPS 分享链接", text: $link).disabled(state.busy)
                        .onChange(of: link) { state.clear(); inputError = nil }
                    SecureField("提取码（无保护的链接可留空）", text: $passcode).disabled(state.busy)
                        .onChange(of: passcode) { state.clear(); inputError = nil }
                    Button("读取分享…") { prepareRead() }.disabled(state.busy || link.isEmpty)
                    if let metadata = state.metadata {
                        Divider()
                        Text(metadata.filename).font(.headline).lineLimit(3)
                        Text(metadata.target.origin.url.absoluteString).textSelection(.enabled).foregroundStyle(.secondary)
                        Text(metadata.isDirectory ? "文件夹，将下载 ZIP 压缩包" : metadata.size.formatted(.byteCount(style: .file)))
                        Text(metadata.expiresAt.map { "到期：\($0.formatted(date: .numeric, time: .shortened))" } ?? "不自动到期")
                        Text("\(metadata.requiresPasscode ? "需要提取码" : "无需提取码") · 已下载 \(metadata.downloadCount) 次\(metadata.maxDownloads == 0 ? "，不限次数" : "，上限 \(metadata.maxDownloads) 次")")
                        if let receipt = state.receipt {
                            Label("文件已验证并保存", systemImage: "checkmark.circle")
                            Text(receipt.url.path).textSelection(.enabled)
                            Text(receipt.bytes.formatted(.byteCount(style: .file))).foregroundStyle(.secondary)
                        } else if !state.downloading {
                            Button("下载分享文件…") { chooseDestination(metadata) }
                                .disabled(state.busy || metadata.maxDownloads > 0 && metadata.downloadCount >= metadata.maxDownloads || metadata.expiresAt.map { $0 <= Date() } ?? false)
                        }
                        Text("每次开始下载可能消耗一次额度，中断后重新下载可能再次计次。此下载不自动重试或恢复，关闭窗口后不保留链接和提取码。已存在的本地文件不会被覆盖；重名时使用新名称。")
                            .font(.callout).foregroundStyle(.secondary)
                    }
                    if state.downloading {
                        ProgressView(value: Double(state.received), total: Double(max(1, state.total)))
                        Text(state.cancelling ? "正在取消并清理临时文件…" : state.verifying ? "正在验证完整性…" : "已接收 \(state.received.formatted(.byteCount(style: .file))) / \(state.total.formatted(.byteCount(style: .file)))")
                            .foregroundStyle(.secondary)
                    } else if state.busy { ProgressView("正在读取分享") }
                    if let error = inputError ?? state.error { Text(error).foregroundStyle(.secondary) }
                }.frame(maxWidth: .infinity, alignment: .leading)
            }.layoutPriority(-1)
            HStack {
                if state.busy { Button("取消") { state.cancel() }.disabled(state.cancelling) }
                Spacer()
                Button("关闭") { dismiss() }.keyboardShortcut(.cancelAction).disabled(state.busy)
            }
        }.padding(24).frame(width: 560, height: 540)
        .interactiveDismissDisabled(state.busy)
        .onDisappear { state.clear(); link = ""; passcode = "" }
        .confirmationDialog("确认分享服务器", isPresented: Binding(get: { pendingRead != nil }, set: { if !$0 { pendingRead = nil } }), presenting: pendingRead) { target in
            Button("确认读取") {
                let code = passcode
                Task { await state.load(target: target, passcode: code) }
            }
        } message: { target in
            Text("将连接 \(target.origin.url.absoluteString)。\(passcode.isEmpty ? "未提供提取码。" : "本次输入的提取码将仅发送给此服务器。")请确认这是你要访问的分享来源。")
        }
        .confirmationDialog("确认下载分享文件", isPresented: Binding(get: { pendingDownload != nil }, set: { if !$0 { pendingDownload = nil } }), presenting: pendingDownload) { selection in
            Button("确认下载") { Task { await state.download(selection.metadata, to: selection.destination) } }
        } message: { selection in
            Text("从 \(selection.metadata.target.origin.url.absoluteString) 下载「\(selection.metadata.filename)」。本次可能消耗一次下载额度。保存到：\(selection.destination.path)。")
        }
    }

    private func prepareRead() {
        do {
            guard ShareContract.validPasscode(passcode) else { throw PublicShareError.invalidPasscode }
            pendingRead = try PublicShareTarget(link)
            inputError = nil
        } catch { inputError = (error as? PublicShareError)?.localizedDescription ?? "分享链接无法读取。" }
    }

    private func chooseDestination(_ metadata: PublicShareMetadata) {
        let panel = NSSavePanel()
        panel.nameFieldStringValue = metadata.filename + (metadata.isDirectory ? ".zip" : "")
        panel.canCreateDirectories = true
        panel.begin { response in
            guard response == .OK, let destination = panel.url else { return }
            pendingDownload = Selection(metadata: metadata, destination: destination)
        }
    }
}
