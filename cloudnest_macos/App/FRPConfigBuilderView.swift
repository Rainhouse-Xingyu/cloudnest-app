import SwiftUI

struct FRPConfigBuilderView: View {
    let device: FRPDevice
    @Environment(\.dismiss) private var dismiss
    @State private var servers: FRPServerState
    @State private var config: FRPConfigState
    @State private var serverID: Int64 = 0
    @State private var confirmation: FRPConfigSelection?

    init(device: FRPDevice, session: SessionState) {
        self.device = device
        _servers = State(initialValue: FRPServerState(session: session))
        _config = State(initialValue: FRPConfigState(session: session))
    }

    private var selected: FRPServer? { servers.items.first { $0.id == serverID && $0.enabled } }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("生成设备配置").font(.title2)
            Text(device.name).foregroundStyle(.secondary)
            Picker("服务器", selection: $serverID) {
                Text("请选择已启用服务器").tag(Int64(0))
                ForEach(servers.items.filter(\.enabled)) { server in
                    Text("\(server.name) · 版本 \(String(server.revisionID))").tag(server.id)
                }
            }.disabled(config.busy || config.submitted)
            HStack {
                Button("重新读取") { Task { serverID = 0; config.clearPreview(); await servers.load() } }
                Button("下一页服务器") {
                    Task { serverID = 0; config.clearPreview(); await servers.load(afterID: servers.nextAfterID) }
                }.disabled(servers.items.count < 50 || servers.nextAfterID == 0)
                Spacer()
                Button("预览配置") {
                    guard let server = selected else { return }
                    let selection = FRPConfigSelection(device_id: device.id, server_id: server.id, server_revision_id: server.revisionID)
                    Task { await config.loadPreview(selection) }
                }.disabled(selected == nil)
            }.disabled(servers.busy || config.busy || config.submitted)
            if let error = servers.error { Text(error).foregroundStyle(.secondary) }
            if let error = config.error { Text(error).foregroundStyle(.secondary) }
            if config.busy || servers.busy { ProgressView().controlSize(.small) }
            ScrollView([.vertical, .horizontal]) {
                if let text = config.preview {
                    Text(text).font(.system(.body, design: .monospaced)).textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                } else if let saved = config.saved {
                    Text("已保存配置 \(String(saved.id))。尚未部署，请返回配置历史核对后另行确认部署。")
                } else {
                    Text("预览只显示脱敏内容，不保存、不部署。服务器未配置或读取失败时，不显示旧预览。")
                        .foregroundStyle(.secondary)
                }
            }
            .frame(minHeight: 70, maxHeight: .infinity).layoutPriority(-1)
            Text("预览是当前映射的采样。保存时会重新读取设备的启用映射并校验已选服务器版本；映射期间有变化时，保存内容可能与先前预览不同。保存后需核对，再确认部署。")
                .font(.callout).foregroundStyle(.secondary)
            HStack {
                Spacer()
                Button("关闭") { dismiss() }.keyboardShortcut(.cancelAction).disabled(config.busy && config.submitted)
                Button("保存配置…") { confirmation = config.selection }
                    .disabled(config.preview == nil || config.busy || config.submitted)
            }.fixedSize(horizontal: false, vertical: true)
        }
        .padding(24).frame(width: 560).frame(minHeight: 420, idealHeight: 520)
        .task { await servers.load() }
        .onChange(of: serverID) { config.clearPreview() }
        .onDisappear { servers.cancel(); config.clearPreview() }
        .confirmationDialog("保存设备配置", isPresented: Binding(
            get: { confirmation != nil }, set: { if !$0 { confirmation = nil } }
        ), titleVisibility: .visible, presenting: confirmation) { selection in
            Button("确认生成并保存") { Task { await config.save(expectedSelection: selection) } }
        } message: { selection in
            Text("设备：\(device.name)，服务器 \(String(selection.server_id))、版本 \(String(selection.server_revision_id))。保存时重新读取当前启用映射，追加新配置，不覆盖历史、不自动部署。")
        }
    }
}
