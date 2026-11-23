import SwiftUI

struct FRPMappingEditor: View {
    let session: SessionState
    let device: FRPDevice
    let original: FRPMapping?
    private let identity: TransferIdentity?
    @Environment(\.dismiss) private var dismiss
    @State private var servers: FRPServerState
    @State private var serverID: Int64
    @State private var kind: Kind
    @State private var localPort: String
    @State private var remotePort: String
    @State private var host: String
    @State private var automatic: Bool
    @State private var enabled: Bool
    @State private var busy = false
    @State private var submitted = false
    @State private var error: String?
    @State private var confirming = false
    private enum Kind: String, CaseIterable { case tcp = "TCP", http = "HTTP", https = "HTTPS" }

    init(session: SessionState, device: FRPDevice, original: FRPMapping?) {
        self.session = session; self.device = device; self.original = original; identity = session.transferIdentity
        _servers = State(initialValue: FRPServerState(session: session))
        _serverID = State(initialValue: original?.serverID ?? 0)
        let domain = original?.domain ?? ""
        _kind = State(initialValue: domain.isEmpty ? .tcp : domain.lowercased().hasPrefix("https://") ? .https : .http)
        _host = State(initialValue: domain.lowercased().hasPrefix("https://") ? String(domain.dropFirst(8)) : domain.lowercased().hasPrefix("http://") ? String(domain.dropFirst(7)) : domain)
        _localPort = State(initialValue: original.map { String($0.localPort) } ?? "8080")
        _remotePort = State(initialValue: original.map { String($0.remotePort) } ?? "")
        _automatic = State(initialValue: original == nil || (original?.serverID ?? 0) > 0 && !domain.isEmpty)
        _enabled = State(initialValue: original?.status == 1 || original == nil)
    }

    private var needsRemotePort: Bool { kind == .tcp || original?.serverID == 0 }
    private var canAllocate: Bool { original == nil || (original?.serverID ?? 0) > 0 && !(original?.domain.isEmpty ?? true) && kind == .tcp }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text(original == nil ? "添加映射" : "编辑映射").font(.title2)
            Text(device.name).foregroundStyle(.secondary)
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    Form {
                        if original == nil {
                            Picker("服务器", selection: $serverID) {
                                Text("请选择已启用服务器").tag(Int64(0))
                                ForEach(servers.items.filter(\.enabled)) { Text($0.name).tag($0.id) }
                            }
                        } else {
                            LabeledContent("固定服务器作用域", value: serverID == 0 ? "旧全局记录" : String(serverID))
                        }
                        Picker("类型", selection: $kind) { ForEach(Kind.allCases, id: \.self) { Text($0.rawValue).tag($0) } }
                        TextField("本地端口", text: $localPort)
                        if kind != .tcp { TextField("域名（不带协议或路径）", text: $host) }
                        if needsRemotePort {
                            if canAllocate { Toggle("自动分配远程端口", isOn: $automatic) }
                            if !canAllocate || !automatic { TextField("远程端口", text: $remotePort) }
                        }
                        Toggle("映射启用", isOn: $enabled)
                    }.disabled(busy || submitted)
                    if original == nil {
                        HStack {
                            Button("重新读取服务器") { Task { serverID = 0; await servers.load() } }
                            Button("下一页服务器") { Task { serverID = 0; await servers.load(afterID: servers.nextAfterID) } }
                                .disabled(servers.items.count < 50 || servers.nextAfterID == 0)
                        }.disabled(busy || submitted || servers.busy)
                        if let server = servers.items.first(where: { $0.id == serverID }) {
                            Text("上次读取的 TCP 范围 \(String(server.tcpStart))–\(String(server.tcpEnd))；HTTP \(String(server.httpPort))，HTTPS \(String(server.httpsPort))。实际按保存时策略校验。")
                                .font(.callout).foregroundStyle(.secondary)
                        }
                        if let error = servers.error { Text(error).foregroundStyle(.secondary) }
                    }
                    if original?.serverID == 0 {
                        Text("旧全局映射的 HTTP/S 也保留远程端口。作用域不能改为其他服务器；需要新作用域时请另建映射。")
                            .font(.callout).foregroundStyle(.secondary)
                    }
                    if kind == .https {
                        Text("HTTPS 使用 SNI 透传，本地服务需提供 TLS；此操作不会签发证书。")
                            .font(.callout).foregroundStyle(.secondary)
                    }
                    Text("保存或移除映射不会切换正在运行的隧道。修改后须重新生成并确认部署配置；旧配置与运行占用仍按服务器规则保留。")
                        .font(.callout).foregroundStyle(.secondary)
                    if let error { Text(error).foregroundStyle(.secondary) }
                }.frame(maxWidth: .infinity, alignment: .leading)
            }.frame(minHeight: 80, maxHeight: .infinity).layoutPriority(-1)
            HStack {
                if busy || servers.busy { ProgressView().controlSize(.small) }
                Spacer()
                Button("取消") { dismiss() }.keyboardShortcut(.cancelAction).disabled(busy)
                Button("保存…") { confirming = true }.keyboardShortcut(.defaultAction).disabled(busy || submitted || original == nil && serverID == 0)
            }.fixedSize(horizontal: false, vertical: true)
        }
        .padding(24).frame(width: 540).frame(minHeight: 420, idealHeight: 540)
        .task { if original == nil { await servers.load() } }
        .onDisappear { servers.cancel() }
        .confirmationDialog("保存设备映射", isPresented: $confirming, titleVisibility: .visible) {
            Button("确认保存") { Task { await save() } }
        } message: {
            Text("设备：\(device.name)，服务器作用域 \(String(serverID))，\(kind.rawValue)，本地端口 \(localPort)。保存仅改变映射资料，不自动部署。")
        }
    }

    private func save() async {
        guard !busy, !submitted else { return }
        error = nil
        guard let local = Int(localPort), (1...65535).contains(local) else { error = "请填写 1–65535 的本地端口。"; return }
        let remote: Int?
        if needsRemotePort {
            if canAllocate && automatic { remote = nil }
            else {
                guard let value = Int(remotePort), (1...65535).contains(value) else { error = "请填写 1–65535 的远程端口。"; return }
                remote = value
            }
        } else { remote = nil }
        let host = host.trimmingCharacters(in: .whitespacesAndNewlines)
        guard kind == .tcp || !host.isEmpty else { error = "请填写域名。"; return }
        let domain = kind == .tcp ? "" : (kind == .https ? "https://" : "http://") + host
        do { try session.requireTransferIdentity(identity) }
        catch { self.error = (error as? ServerError)?.localizedDescription; return }
        busy = true; submitted = true
        defer { busy = false }
        do {
            _ = try await session.writeFRPMapping(original: original, deviceID: device.id, serverID: serverID,
                localPort: local, remotePort: remote, domain: domain, enabled: enabled, expectedIdentity: identity)
            try session.requireTransferIdentity(identity)
            dismiss()
        } catch {
            if error as? ServerError == .conflict {
                self.error = "映射内容、端口/域名或设备/服务器策略发生冲突，请关闭并刷新后核对。"
            } else if let rejection = error as? ServerError, [.invalidOperation, .forbidden, .rateLimited].contains(rejection) {
                submitted = false
                self.error = rejection == .invalidOperation ? "请核对端口和域名格式、长度及服务器策略。" : rejection.localizedDescription
            } else {
                self.error = "未取得映射保存成功回执，请关闭并刷新核对，避免重复创建。"
            }
        }
    }
}
