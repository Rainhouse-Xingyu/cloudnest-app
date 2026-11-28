import SwiftUI

struct FRPDeviceEditor: View {
    let session: SessionState
    let original: FRPDevice?
    private let identity: TransferIdentity?
    @Environment(\.dismiss) private var dismiss
    @State private var name: String
    @State private var address: String
    @State private var enabled: Bool
    @State private var busy = false
    @State private var submitted = false
    @State private var error: String?
    @State private var confirming = false

    init(session: SessionState, original: FRPDevice?) {
        self.session = session; self.original = original; identity = session.transferIdentity
        _name = State(initialValue: original?.name ?? "")
        _address = State(initialValue: original?.address ?? "")
        _enabled = State(initialValue: original?.status == 1 || original == nil)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text(original == nil ? "添加设备" : "编辑设备").font(.title2)
            Form {
                TextField("名称", text: $name)
                TextField("设备地址（可选）", text: $address)
                if original != nil { Toggle("设备启用", isOn: $enabled) }
            }.disabled(busy || submitted)
            Text("设备地址仅用于管理标记。新增设备默认启用，注册 Agent 后才有心跳；启用或停用资料不代表隧道已启动或停止。")
                .font(.callout).foregroundStyle(.secondary)
            if original != nil {
                Text("保存时核对已读名称、地址和启用状态；其他客户端修改后需刷新再编辑。留空地址会明确清除该标记。")
                    .font(.callout).foregroundStyle(.secondary)
            }
            if let error { Text(error).foregroundStyle(.secondary) }
            HStack {
                if busy { ProgressView().controlSize(.small) }
                Spacer()
                Button("取消") { dismiss() }.keyboardShortcut(.cancelAction).disabled(busy)
                Button("保存…") { confirming = true }.keyboardShortcut(.defaultAction).disabled(busy || submitted)
            }
        }
        .padding(24).frame(width: 500)
        .confirmationDialog("保存设备资料", isPresented: $confirming, titleVisibility: .visible) {
            Button("确认保存") { Task { await save() } }
        } message: {
            Text("设备：\(name)。\(original?.address.isEmpty == false && address.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? "将清除原设备地址。" : "")保存不会自动注册 Agent 或部署配置。")
        }
    }

    private func save() async {
        guard !busy, !submitted else { return }
        let name = name.trimmingCharacters(in: .whitespacesAndNewlines)
        let address = address.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !name.isEmpty else { error = "请填写设备名称。"; return }
        do { try session.requireTransferIdentity(identity) }
        catch { self.error = (error as? ServerError)?.localizedDescription; return }
        busy = true; submitted = true; error = nil
        defer { busy = false }
        do {
            _ = try await session.writeFRPDevice(original: original, name: name, address: address, enabled: enabled, expectedIdentity: identity)
            try session.requireTransferIdentity(identity)
            dismiss()
        } catch {
            if error as? ServerError == .conflict {
                self.error = "设备资料已变化，请关闭并刷新，核对最新内容后重新编辑。"
            } else if let rejection = error as? ServerError, [.invalidOperation, .forbidden, .rateLimited].contains(rejection) {
                submitted = false
                self.error = rejection == .invalidOperation ? "请核对设备名称和地址的长度与格式。" : rejection.localizedDescription
            } else {
                self.error = "未取得保存成功回执。请关闭并刷新设备列表核对，避免重复创建。"
            }
        }
    }
}

struct FRPDeviceArchiveView: View {
    let session: SessionState
    let device: FRPDevice
    private let identity: TransferIdentity?
    @Environment(\.dismiss) private var dismiss
    @State private var archive: FRPDeviceArchive?
    @State private var error: String?
    @State private var busy = true
    init(session: SessionState, device: FRPDevice) { self.session = session; self.device = device; identity = session.transferIdentity }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("退役设备详情").font(.title2)
            if busy { ProgressView("正在核对保留记录") }
            if let archive {
                LabeledContent("设备", value: archive.device.name)
                LabeledContent("设备 ID", value: String(archive.device.id))
                LabeledContent("退役时间", value: archive.device.retiredAt ?? "")
                LabeledContent("保留映射数", value: String(archive.mappingCount))
                LabeledContent("最近配置时间", value: archive.latestConfigTime ?? "无记录")
            }
            if let error { Text(error).foregroundStyle(.secondary) }
            Text("设备凭据已撤销，配置、命令和占用保留。此处展示退役元数据，不确认离线隧道已停止，也不自动释放端口或删除历史。")
                .font(.callout).foregroundStyle(.secondary)
            HStack { Spacer(); Button("关闭") { dismiss() }.keyboardShortcut(.cancelAction) }
        }.padding(24).frame(width: 500)
        .task {
            defer { busy = false }
            do {
                try session.requireTransferIdentity(identity)
                let value = try await session.frpDeviceArchive(id: device.id)
                try Task.checkCancellation()
                try session.requireTransferIdentity(identity)
                archive = value
            } catch {
                if !(error is CancellationError) { self.error = (error as? ServerError)?.localizedDescription ?? "无法读取保留记录。" }
            }
        }
    }
}
