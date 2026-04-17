import SwiftUI

struct FRPSettingsView: View {
    @StateObject private var viewModel = FRPSettingsViewModel()
    @State private var showAddConfigPage: Bool = false

    var body: some View {
        List {
            Section("在线设备连接状态") {
                if viewModel.devices.isEmpty {
                    Text("暂无设备")
                        .foregroundStyle(.secondary)
                } else {
                    ForEach(viewModel.devices) { device in
                        HStack(spacing: 12) {
                            Circle()
                                .fill(device.online ? Color.green : Color.gray)
                                .frame(width: 10, height: 10)

                            VStack(alignment: .leading, spacing: 4) {
                                Text(device.deviceName)
                                    .font(.headline)
                                Text(device.deviceIP.isEmpty ? "未设置设备 IP" : device.deviceIP)
                                    .font(.subheadline)
                                    .foregroundStyle(.secondary)
                                Text(connectionStatusText(for: device))
                                    .font(.caption)
                                    .foregroundStyle(device.online ? .green : .secondary)
                            }

                            Spacer()

                            Text(device.enabled ? "启用" : "禁用")
                                .font(.caption)
                                .padding(.horizontal, 8)
                                .padding(.vertical, 4)
                                .background((device.enabled ? Color.blue : Color.gray).opacity(0.14), in: Capsule())
                        }
                        .padding(.vertical, 2)
                    }
                }
            }

            Section {
                Button {
                    showAddConfigPage = true
                } label: {
                    Label("添加新的 frpc 设置", systemImage: "plus.circle.fill")
                }
            }

            if let saveMessage = viewModel.saveMessage {
                Section("保存结果") {
                    Text(saveMessage)
                        .foregroundStyle(.green)
                        .font(.footnote)
                }
            }

            if let errorMessage = viewModel.errorMessage {
                Section("错误提示") {
                    Text(errorMessage)
                        .foregroundStyle(.red)
                        .font(.footnote)
                }
            }
        }
        .scrollContentBackground(.hidden)
        .background(Color.clear)
        .liquidGlassPage()
        .navigationTitle("FRP设置")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button {
                    Task {
                        await viewModel.reload()
                    }
                } label: {
                    Image(systemName: "arrow.clockwise")
                }
            }
        }
        .overlay {
            if viewModel.isLoading {
                ProgressView("加载中...")
                    .padding(14)
                    .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 12))
            }
        }
        .navigationDestination(isPresented: $showAddConfigPage) {
            FRPAddConfigView(viewModel: viewModel)
        }
        .onAppear {
            viewModel.onAppear()
        }
    }

    private func connectionStatusText(for device: FRPDeviceStatusItem) -> String {
        if device.online {
            let heartbeatText = device.lastHeartbeat.isEmpty ? "刚刚" : device.lastHeartbeat
            return "在线 · 最近心跳：\(heartbeatText)"
        }
        return "离线"
    }
}

private struct FRPAddConfigView: View {
    @ObservedObject var viewModel: FRPSettingsViewModel
    @Environment(\.dismiss) private var dismiss

    @State private var form: FRPConfigFormInput = FRPConfigFormInput()
    @State private var isSaving: Bool = false

    var body: some View {
        Form {
            Section("目标设备") {
                Picker("设备", selection: $form.deviceID) {
                    Text("请选择设备")
                        .tag(Int64?.none)
                    ForEach(viewModel.devices) { device in
                        Text(device.deviceName)
                            .tag(Optional(device.id))
                    }
                }
            }

            Section("FRPC 服务端") {
                TextField("server_addr", text: $form.serverAddr)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                TextField("server_port", text: $form.serverPort)
                    .keyboardType(.numberPad)
                SecureField("token（可选）", text: $form.token)
            }

            Section {
                Button {
                    Task {
                        isSaving = true
                        let success = await viewModel.saveConfig(input: form)
                        isSaving = false
                        if success {
                            dismiss()
                        }
                    }
                } label: {
                    HStack {
                        Text("保存并生成 frpc.yaml")
                        Spacer()
                        if isSaving {
                            ProgressView()
                        }
                    }
                }
                .disabled(isSaving)
            }
        }
        .navigationTitle("新增 FRPC 设置")
        .navigationBarTitleDisplayMode(.inline)
        .onAppear {
            if form.deviceID == nil {
                form.deviceID = viewModel.devices.first?.id
            }
        }
    }
}

struct FRPSettingsView_Previews: PreviewProvider {
    static var previews: some View {
        NavigationStack {
            FRPSettingsView()
        }
    }
}
