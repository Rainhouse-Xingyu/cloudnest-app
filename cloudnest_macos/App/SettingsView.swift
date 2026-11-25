import SwiftUI

struct SettingsView: View {
    let session: SessionState
    @State private var state: SettingsState
    @State private var selected: Selection?
    @State private var editingWindow = false
    private struct Selection: Identifiable {
        let key: SystemSettingKey
        let original: String?
        var id: String { key.rawValue }
    }

    init(session: SessionState) { self.session = session; _state = State(initialValue: SettingsState(session: session)) }

    var body: some View {
        Form {
            if state.busy { ProgressView("正在读取设置") }
            if let error = state.error { Text(error).foregroundStyle(.secondary) }
            if let window = state.window {
                Section("个人高峰时段") {
                    LabeledContent("来源", value: window.source == "user" ? "个人配置" : "继承系统显示值")
                    LabeledContent("状态", value: window.enabled ? "开启" : "关闭")
                    LabeledContent("时段", value: window.start.isEmpty && window.end.isEmpty ? "未配置" : "\(window.start) – \(window.end)")
                    Text("按服务器本地时间。个人高峰仅能收紧系统速率上限；关闭个人高峰不会关闭系统高峰。跨午夜有效，相同起止时间表示全天。")
                        .foregroundStyle(.secondary)
                    Button("编辑个人高峰时段…") { editingWindow = true }
                        .disabled(window.expected == nil || state.busy)
                    if window.expected == nil { Text("服务器未提供条件保存支持，请升级服务器后编辑。").foregroundStyle(.secondary) }
                }
            }
            if let settings = state.system {
                Section("系统设置") {
                    Text(session.account?.isAdmin == true ? "管理员可修改。保存时比较已读取的内容，冲突后须刷新。" : "当前账户仅查看，系统设置由管理员修改。")
                        .foregroundStyle(.secondary)
                    ForEach(SystemSettingKey.allCases) { key in
                        let original = settings.items.first { $0.key == key }?.value
                        HStack(alignment: .top) {
                            VStack(alignment: .leading, spacing: 4) {
                                Text(key.title)
                                Text(original ?? "未显式配置").textSelection(.enabled).foregroundStyle(.secondary)
                                if let original, !key.accepts(original) { Text("当前值不符合受支持范围，请由管理员核对。").foregroundStyle(.secondary) }
                            }
                            Spacer()
                            if session.account?.isAdmin == true {
                                Button("编辑…") { selected = Selection(key: key, original: original) }
                                    .disabled(!settings.conditionalWrites || state.busy)
                                    .accessibilityLabel("编辑\(key.title)")
                            }
                        }
                    }
                    if !settings.conditionalWrites { Text("服务器未确认系统范围和条件保存能力，编辑已停用。").foregroundStyle(.secondary) }
                    Text("设置由服务器与支持这些选项的客户端执行。速率按每次传输限制，修改不会让已有任务重新开始，也不会立即删除历史文件。")
                        .foregroundStyle(.secondary)
                }
            }
        }.formStyle(.grouped)
        .toolbar { Button("刷新设置", systemImage: "arrow.clockwise") { Task { await state.load() } }.disabled(state.busy) }
        .task { await state.load() }
        .onDisappear { state.cancel() }
        .sheet(item: $selected, onDismiss: { Task { await state.load() } }) { selection in
            SystemSettingEditor(session: session, identity: state.identity, key: selection.key, original: selection.original)
        }
        .sheet(isPresented: $editingWindow, onDismiss: { Task { await state.load() } }) {
            if let window = state.window { RateWindowEditor(session: session, identity: state.identity, original: window) }
        }
    }
}

private struct SystemSettingEditor: View {
    @Environment(\.dismiss) private var dismiss
    let session: SessionState
    let identity: TransferIdentity?
    let key: SystemSettingKey
    let original: String?
    @State private var value: String
    @State private var editor = SettingsEditState()
    @State private var confirming = false

    init(session: SessionState, identity: TransferIdentity?, key: SystemSettingKey, original: String?) {
        self.session = session; self.identity = identity; self.key = key; self.original = original
        _value = State(initialValue: original ?? "")
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text(key.title).font(.title2)
            Text(key.guidance).foregroundStyle(.secondary)
            TextField("新值", text: $value).disabled(editor.busy || editor.locked)
            Text("当前值：\(original ?? "未显式配置")").foregroundStyle(.secondary)
            if let error = editor.error { Text(error).foregroundStyle(.secondary) }
            HStack {
                Button("关闭") { dismiss() }.keyboardShortcut(.cancelAction).disabled(editor.busy)
                Spacer()
                Button("保存…") { confirming = true }.disabled(editor.busy || editor.locked || !key.accepts(value) || value == original)
            }
        }.padding(24).frame(width: 500)
        .interactiveDismissDisabled(editor.busy)
        .onChange(of: editor.saved) { if editor.saved { dismiss() } }
        .confirmationDialog("确认修改系统设置", isPresented: $confirming, titleVisibility: .visible) {
            Button("确认保存") {
                let confirmed = value
                Task { await editor.save { try await session.saveSystemSetting(key: key, value: confirmed, original: original, expectedIdentity: identity) } }
            }
        } message: { Text("将「\(key.title)」从「\(original ?? "未显式配置")」改为「\(value)」。已有文件会保留，其他客户端的更新可能使本次保存被拒绝。") }
    }
}

private struct RateWindowEditor: View {
    @Environment(\.dismiss) private var dismiss
    let session: SessionState
    let identity: TransferIdentity?
    let original: UserRateWindow
    @State private var enabled: Bool
    @State private var start: String
    @State private var end: String
    @State private var editor = SettingsEditState()
    @State private var confirming = false

    init(session: SessionState, identity: TransferIdentity?, original: UserRateWindow) {
        self.session = session; self.identity = identity; self.original = original
        _enabled = State(initialValue: original.enabled); _start = State(initialValue: original.start); _end = State(initialValue: original.end)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("个人高峰时段").font(.title2)
            Toggle("启用个人高峰时段", isOn: $enabled).disabled(editor.busy || editor.locked)
            TextField("开始（HH:MM）", text: $start).disabled(!enabled || editor.busy || editor.locked)
            TextField("结束（HH:MM）", text: $end).disabled(!enabled || editor.busy || editor.locked)
            Text("按服务器本地时间；跨午夜有效，相同起止时间表示全天。关闭个人高峰仍保留系统高峰限制。保存个人配置不会提高速率上限。").foregroundStyle(.secondary)
            if let error = editor.error { Text(error).foregroundStyle(.secondary) }
            HStack {
                Button("关闭") { dismiss() }.keyboardShortcut(.cancelAction).disabled(editor.busy)
                Spacer()
                Button("保存…") { confirming = true }
                    .disabled(editor.busy || editor.locked || enabled && (!SettingsContract.validTime(start) || !SettingsContract.validTime(end)))
            }
        }.padding(24).frame(width: 500)
        .interactiveDismissDisabled(editor.busy)
        .onChange(of: editor.saved) { if editor.saved { dismiss() } }
        .confirmationDialog("确认个人高峰时段", isPresented: $confirming, titleVisibility: .visible) {
            Button("确认保存") {
                let confirmed = (enabled, start, end)
                Task { await editor.save { try await session.saveRateWindow(original: original, enabled: confirmed.0, start: confirmed.1, end: confirmed.2, expectedIdentity: identity) } }
            }
        } message: { Text(enabled ? "启用 \(start) – \(end)（服务器本地时间）。个人时段只能叠加收紧系统上限。" : "关闭个人高峰时段。系统高峰仍然生效。") }
    }
}
