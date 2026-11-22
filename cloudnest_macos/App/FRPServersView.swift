import SwiftUI

struct FRPServersView: View {
    let session: SessionState
    @State private var state: FRPServerState
    @State private var editor: Editor?
    private struct Editor: Identifiable { let id = UUID(); let server: FRPServer? }

    init(session: SessionState) { self.session = session; _state = State(initialValue: FRPServerState(session: session)) }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            if let error = state.error { Text(error).foregroundStyle(.secondary).padding() }
            List(state.items) { server in
                HStack {
                    VStack(alignment: .leading, spacing: 5) {
                        Text(server.name).font(.headline)
                        Text("\(server.address) · 控制端口 \(String(server.port))")
                        Text("TCP \(String(server.tcpStart))–\(String(server.tcpEnd)) · \(server.enabled ? "已启用" : "已停用")")
                            .foregroundStyle(.secondary)
                        Text("版本 \(String(server.revisionID)) · \(server.tokenConfigured ? "已配置认证 token" : "未配置认证 token")")
                            .font(.caption).foregroundStyle(.secondary)
                    }
                    Spacer()
                    Button("编辑…") { editor = Editor(server: server) }
                        .buttonStyle(.borderless).accessibilityLabel("编辑服务器 \(server.name)")
                }
                .padding(.vertical, 6)
            }
            .overlay {
                if state.busy { ProgressView("正在读取服务器") }
                else if state.loaded && state.items.isEmpty && state.error == nil {
                    ContentUnavailableView("没有可见服务器", systemImage: "server.rack")
                }
            }
            HStack {
                Text("每页最多 50 台，翻页替换当前列表。修改资料不会自动部署配置。")
                    .font(.callout).foregroundStyle(.secondary)
                Spacer()
                Button("第一页") { Task { await state.load() } }.disabled(state.busy || state.afterID == 0)
                Button("下一页") { Task { await state.load(afterID: state.nextAfterID) } }
                    .disabled(state.busy || state.items.count < 50 || state.nextAfterID == 0)
            }.padding()
        }
        .toolbar {
            ToolbarItem { Button("添加服务器", systemImage: "plus") { editor = Editor(server: nil) } }
            ToolbarItem { Button("刷新服务器", systemImage: "arrow.clockwise") { Task { await state.load() } }.disabled(state.busy) }
        }
        .task { await state.load() }
        .onDisappear { state.cancel() }
        .sheet(item: $editor, onDismiss: { Task { await state.load() } }) { item in
            FRPServerEditor(session: session, server: item.server)
        }
    }
}

private struct FRPServerEditor: View {
    let session: SessionState
    let server: FRPServer?
    private let identity: TransferIdentity?
    @Environment(\.dismiss) private var dismiss
    @State private var name: String
    @State private var address: String
    @State private var port: String
    @State private var tcpStart: String
    @State private var tcpEnd: String
    @State private var httpPort: String
    @State private var httpsPort: String
    @State private var enabled: Bool
    @State private var tokenMode = TokenMode.keep
    @State private var token = ""
    @State private var busy = false
    @State private var submitted = false
    @State private var error: String?
    @State private var confirming = false
    private enum TokenMode: String, CaseIterable { case keep = "保留现有 token", replace = "替换 token", clear = "清除 token" }

    init(session: SessionState, server: FRPServer?) {
        self.session = session; self.server = server; identity = session.transferIdentity
        _name = State(initialValue: server?.name ?? "")
        _address = State(initialValue: server?.address ?? "")
        _port = State(initialValue: String(server?.port ?? 7000))
        _tcpStart = State(initialValue: String(server?.tcpStart ?? 20000))
        _tcpEnd = State(initialValue: String(server?.tcpEnd ?? 20999))
        _httpPort = State(initialValue: String(server?.httpPort ?? 0))
        _httpsPort = State(initialValue: String(server?.httpsPort ?? 0))
        _enabled = State(initialValue: server?.enabled ?? true)
        _tokenMode = State(initialValue: server == nil ? .replace : .keep)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text(server == nil ? "添加 FRP 服务器" : "编辑 FRP 服务器").font(.title2)
            Form {
                TextField("名称", text: $name)
                TextField("主机名或 IP", text: $address).disabled(server != nil)
                TextField("控制端口", text: $port).disabled(server != nil)
                TextField("TCP 起始端口", text: $tcpStart)
                TextField("TCP 结束端口", text: $tcpEnd)
                TextField("HTTP 端口（0 停用）", text: $httpPort)
                TextField("HTTPS 端口（0 停用）", text: $httpsPort)
                Toggle("服务器启用", isOn: $enabled)
                if server != nil {
                    Picker("认证 token", selection: $tokenMode) { ForEach(TokenMode.allCases, id: \.self) { Text($0.rawValue).tag($0) } }
                } else {
                    Toggle("使用认证 token", isOn: Binding(get: { tokenMode == .replace }, set: { tokenMode = $0 ? .replace : .clear }))
                }
                if tokenMode == .replace { SecureField("新的 token", text: $token) }
            }
            .disabled(busy || submitted)
            if let server {
                Text("以已读取版本 \(String(server.revisionID)) 保存；版本冲突需关闭并刷新后重新核对。主机和控制端口固定，变更请另建服务器。")
                    .font(.callout).foregroundStyle(.secondary)
            }
            Text("保存不会修改 frps 进程或部署隧道。现有配置保留原服务器版本；更换 token 后需另行生成并部署新配置。")
                .font(.callout).foregroundStyle(.secondary)
            if let error { Text(error).foregroundStyle(.secondary) }
            HStack {
                if busy { ProgressView().controlSize(.small) }
                Spacer()
                Button("取消") { token = ""; dismiss() }.keyboardShortcut(.cancelAction).disabled(busy)
                Button("保存…") { confirming = true }.keyboardShortcut(.defaultAction).disabled(busy || submitted)
            }
        }
        .padding(24).frame(width: 540)
        .confirmationDialog("保存服务器资料", isPresented: $confirming, titleVisibility: .visible) {
            Button("确认保存") { Task { await save() } }
        } message: { Text("请确认 \(name) 的端口策略和 token 操作。停用服务器不会自动停止正在运行的隧道。") }
        .onDisappear { token = "" }
        .onChange(of: tokenMode) { token = "" }
    }

    private func save() async {
        guard !busy, !submitted else { return }
        error = nil
        guard let port = Int(port), let start = Int(tcpStart), let end = Int(tcpEnd),
              let http = Int(httpPort), let https = Int(httpsPort), tokenMode != .replace || !token.isEmpty else {
            error = "请填写有效端口；替换 token 时必须填写新值。"; return
        }
        let input = FRPServerInput(name: name.trimmingCharacters(in: .whitespacesAndNewlines),
            server_addr: address.trimmingCharacters(in: .whitespacesAndNewlines), server_port: port,
            tcp_start: start, tcp_end: end, http_port: http, https_port: https, enabled: enabled,
            expected_revision_id: server?.revisionID ?? 0, token: tokenMode == .keep ? nil : tokenMode == .clear ? "" : token)
        do { try input.validate(updating: server != nil); try session.requireTransferIdentity(identity) }
        catch { self.error = (error as? ServerError)?.localizedDescription ?? "请核对服务器资料。"; return }
        busy = true
        submitted = true
        defer { busy = false; token = "" }
        do {
            _ = try await session.writeFRPServer(id: server?.id, input: input, expectedIdentity: identity)
            try session.requireTransferIdentity(identity)
            dismiss()
        } catch {
            if error as? ServerError == .conflict {
                self.error = "服务器版本、端口策略或地址发生冲突。请关闭并刷新，核对最新资料后重新编辑。"
            } else if let rejection = error as? ServerError, [.invalidOperation, .forbidden, .rateLimited].contains(rejection) {
                submitted = false
                self.error = rejection == .invalidOperation ? "服务器拒绝了资料，请核对主机、端口范围和 token 后重试。" : rejection.localizedDescription
            } else {
                self.error = "未取得保存成功回执。\((error as? ServerError)?.localizedDescription ?? "连接未能完成。")请关闭并刷新服务器列表核对，避免重复创建。"
            }
        }
    }
}
