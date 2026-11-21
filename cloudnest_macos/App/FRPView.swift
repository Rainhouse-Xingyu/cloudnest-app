import SwiftUI

struct FRPView: View {
    let session: SessionState
    @State private var browser: FRPBrowserState
    @State private var selected: FRPDevice?

    init(session: SessionState) {
        self.session = session
        _browser = State(initialValue: FRPBrowserState(session: session))
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            if let error = browser.error {
                Label(error, systemImage: "exclamationmark.circle").foregroundStyle(.secondary).padding()
            }
            List(browser.visibleDevices) { device in
                HStack {
                    VStack(alignment: .leading, spacing: 5) {
                        Text(device.name).font(.headline)
                        Text(device.address.isEmpty ? "未填写设备地址" : device.address).foregroundStyle(.secondary)
                        Text(device.status == 1 ? "设备已启用" : "设备已停用").font(.caption).foregroundStyle(.secondary)
                    }
                    Spacer()
                    Button("查看详情") { selected = device }
                        .buttonStyle(.borderless)
                        .accessibilityLabel("查看 \(device.name) 的 FRP 详情")
                }
                .padding(.vertical, 6)
                .accessibilityElement(children: .contain)
            }
            .overlay {
                if browser.busy && !browser.loaded { ProgressView("正在读取设备") }
                else if browser.loaded && browser.visibleDevices.isEmpty && browser.error == nil {
                    ContentUnavailableView(browser.devices.isEmpty ? "没有可见设备" : "没有匹配设备", systemImage: "network")
                }
            }
            Text("设备启用状态与 Agent 在线、隧道注册及本地服务可达性分别查看。")
                .font(.callout).foregroundStyle(.secondary).padding()
        }
        .searchable(text: $browser.filter, prompt: "筛选设备名称或地址")
        .toolbar {
            ToolbarItem {
                Button("刷新设备", systemImage: "arrow.clockwise") { Task { await browser.reload() } }
                    .disabled(browser.busy)
            }
        }
        .task { await browser.reload() }
        .sheet(item: $selected) { device in
            FRPDetailView(device: device, session: session)
        }
    }
}

private struct FRPDetailView: View {
    @Environment(\.dismiss) private var dismiss
    @State private var state: FRPDetailState
    @State private var section = FRPSection.status

    init(device: FRPDevice, session: SessionState) {
        _state = State(initialValue: FRPDetailState(device: device, session: session))
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text(state.device.name).font(.title2)
            Picker("设备信息", selection: $section) {
                ForEach(FRPSection.allCases, id: \.self) { Text($0.rawValue).tag($0) }
            }
            .pickerStyle(.segmented)
            HStack {
                Button("刷新", systemImage: "arrow.clockwise") { Task { await state.load(section) } }
                    .disabled(state.busy)
                if let checked = state.checkedAt {
                    Text("查询于 \(checked.formatted(date: .omitted, time: .standard))").font(.caption).foregroundStyle(.secondary)
                }
                Spacer()
                if state.busy { ProgressView().controlSize(.small) }
            }
            if let error = state.error { Label(error, systemImage: "exclamationmark.circle").foregroundStyle(.secondary) }
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    switch state.snapshot {
                    case .status(let status): statusView(status)
                    case .mappings(let items): mappingView(items)
                    case .configs(let items): configView(items)
                    case .commands(let items): commandView(items)
                    case nil: EmptyView()
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .textSelection(.enabled)
            }
            HStack { Spacer(); Button("关闭") { dismiss() }.keyboardShortcut(.cancelAction) }
        }
        .padding(24)
        .frame(minWidth: 480, idealWidth: 580, minHeight: 380, idealHeight: 500)
        .task(id: section) { await state.load(section) }
        .onDisappear { state.cancel() }
    }

    private func statusView(_ status: FRPStatus) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            LabeledContent("上次查询时 Agent", value: status.online ? "在线" : "离线或未注册")
            LabeledContent("最近心跳", value: status.heartbeat ?? "尚无记录")
            LabeledContent("Agent 版本", value: status.clientVersion.isEmpty ? "尚无记录" : status.clientVersion)
            if !status.lastError.isEmpty { Text("最近错误：\(status.lastError)") }
            if status.online, let runtime = status.runtime {
                Divider()
                LabeledContent("隧道采样状态", value: stateName(runtime.state))
                LabeledContent("采样时间", value: runtime.observedAt)
                Text("应注册 \(runtime.expected) · 已注册 \(runtime.registered) · 失败 \(runtime.failed) · 等待 \(runtime.pending)")
                ForEach(Array((status.proxies ?? []).enumerated()), id: \.offset) { _, proxy in
                    Text("映射 \(proxy.mappingID.map(String.init) ?? "—")：\(stateName(proxy.state)) · 本地 TCP：\(upstreamName(proxy.upstream))")
                        .font(.callout)
                }
            } else {
                Text("尚无当前可用的隧道采样结果。Agent 心跳不等于隧道注册成功。")
                    .foregroundStyle(.secondary)
            }
            Text("本地 TCP 可达仅代表端口连接成功，不代表 HTTP、数据库或其他应用协议健康。刷新可查看新的采样。")
                .font(.callout).foregroundStyle(.secondary)
        }
    }

    @ViewBuilder private func mappingView(_ items: [FRPMapping]) -> some View {
        if items.isEmpty { Text("此设备没有映射。") }
        ForEach(items) { mapping in
            VStack(alignment: .leading, spacing: 5) {
                Text("本地端口 \(String(mapping.localPort)) → \(mapping.domain.isEmpty ? String(mapping.remotePort) : mapping.domain)").font(.headline)
                Text("服务器 \(String(mapping.serverID)) · \(mapping.status == 1 ? "映射已启用" : "映射已停用")").foregroundStyle(.secondary)
            }
            Divider()
        }
    }

    @ViewBuilder private func configView(_ items: [FRPConfig]) -> some View {
        Text("配置内容由服务器保管，此处仅显示历史元数据。配置保存不等于已部署。")
            .font(.callout).foregroundStyle(.secondary)
        if items.isEmpty { Text("尚无保存的配置。") }
        ForEach(items) { config in
            VStack(alignment: .leading, spacing: 5) {
                Text("配置 \(String(config.id))").font(.headline)
                Text("服务器 \(String(config.serverID)) · 服务器版本 \(String(config.serverRevisionID))")
                Text(config.createdAt).foregroundStyle(.secondary)
            }
            Divider()
        }
    }

    @ViewBuilder private func commandView(_ items: [FRPCommand]) -> some View {
        Text("最近 20 条命令。排队或送达不等于执行成功，以最终回执与运行采样为准。")
            .font(.callout).foregroundStyle(.secondary)
        if items.isEmpty { Text("尚无命令记录。") }
        ForEach(items) { command in
            VStack(alignment: .leading, spacing: 6) {
                Text("\(actionName(command.action)) · \(commandStatus(command.status))").font(.headline)
                Text("创建于 \(command.createdAt) · 尝试 \(command.attempts) 次").font(.callout)
                if !command.resultCode.isEmpty { Text("执行结果：\(command.resultCode)") }
                if let diagnostic = command.diagnostic { Text("诊断采样：\(stateName(diagnostic.state)) · \(diagnostic.observedAt)") }
                Text("命令 ID：\(command.id)").font(.caption).foregroundStyle(.secondary)
            }
            Divider()
        }
    }

    private func stateName(_ value: String) -> String {
        ["registered": "已注册", "starting": "正在注册", "degraded": "部分失败", "stopped": "已停止",
         "unavailable": "不可用", "pending": "等待注册", "failed": "注册失败"] [value] ?? value
    }
    private func upstreamName(_ value: String?) -> String {
        guard let value, !value.isEmpty else { return "未检测" }
        return ["reachable": "可达", "unreachable": "不可达", "unknown": "未知"] [value] ?? value
    }
    private func actionName(_ value: String) -> String {
        ["deploy_config": "部署配置", "start": "启动", "stop": "停止", "reload": "重载", "diagnose": "诊断", "forget_config": "清除配置"] [value] ?? value
    }
    private func commandStatus(_ value: String) -> String {
        ["pending": "等待执行", "leased": "已送达待回执", "succeeded": "执行成功", "failed": "执行失败",
         "timed_out": "已超时", "cancelled": "已取消"] [value] ?? value
    }
}
