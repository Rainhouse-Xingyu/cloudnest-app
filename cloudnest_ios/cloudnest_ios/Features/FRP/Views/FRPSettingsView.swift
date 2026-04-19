import SwiftUI

private enum FRPSettingsSheet: Identifiable {
    case addConfig
    case addDevice
    case mappingManage

    var id: String {
        switch self {
        case .addConfig:
            return "addConfig"
        case .addDevice:
            return "addDevice"
        case .mappingManage:
            return "mappingManage"
        }
    }
}

struct FRPSettingsView: View {
    @StateObject private var viewModel = FRPSettingsViewModel()
    @State private var activeSheet: FRPSettingsSheet?

    var body: some View {
        List {
            Section("设备状态") {
                if viewModel.devices.isEmpty {
                    Text("暂无设备")
                        .foregroundStyle(.secondary)
                } else {
                    ForEach(viewModel.devices) { device in
                        VStack(alignment: .leading, spacing: 8) {
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

                            HStack(spacing: 12) {
                                Button("映射管理") {
                                    viewModel.selectedDevice = device
                                    activeSheet = .mappingManage
                                }
                                .buttonStyle(.bordered)

                                Button(device.enabled ? "禁用" : "启用") {
                                    Task {
                                        await viewModel.updateDeviceStatus(device: device, enabled: !device.enabled)
                                    }
                                }
                                .buttonStyle(.bordered)

                                Button("删除", role: .destructive) {
                                    Task {
                                        await viewModel.deleteDevice(device: device)
                                    }
                                }
                                .buttonStyle(.bordered)
                            }
                        }
                        .padding(.vertical, 2)
                    }
                }
            }

            Section {
                Button {
                    activeSheet = .addDevice
                } label: {
                    Label("添加设备", systemImage: "desktopcomputer")
                }

                Button {
                    activeSheet = .addConfig
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
        .sheet(item: $activeSheet) { sheet in
            NavigationStack {
                switch sheet {
                case .addConfig:
                    FRPAddConfigView(viewModel: viewModel)
                case .addDevice:
                    FRPAddDeviceView(viewModel: viewModel)
                case .mappingManage:
                    FRPMappingManageView(viewModel: viewModel)
                }
            }
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

private struct FRPAddDeviceView: View {
    @ObservedObject var viewModel: FRPSettingsViewModel
    @Environment(\.dismiss) private var dismiss

    @State private var form: FRPCreateDeviceInput = FRPCreateDeviceInput()
    @State private var isSaving: Bool = false

    var body: some View {
        Form {
            Section("设备信息") {
                TextField("设备名称", text: $form.deviceName)
                TextField("设备 IP（可选）", text: $form.deviceIP)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
            }

            Section {
                Button {
                    Task {
                        isSaving = true
                        let success = await viewModel.createDevice(input: form)
                        isSaving = false
                        if success {
                            dismiss()
                        }
                    }
                } label: {
                    HStack {
                        Text("创建设备")
                        Spacer()
                        if isSaving {
                            ProgressView()
                        }
                    }
                }
                .disabled(isSaving)
            }
        }
        .navigationTitle("新增设备")
        .navigationBarTitleDisplayMode(.inline)
    }
}

private struct FRPMappingManageView: View {
    @ObservedObject var viewModel: FRPSettingsViewModel
    @State private var showAddMappingPage: Bool = false

    var body: some View {
        List {
            if let device = viewModel.selectedDevice {
                Section("当前设备") {
                    VStack(alignment: .leading, spacing: 4) {
                        Text(device.deviceName)
                            .font(.headline)
                        Text(device.deviceIP.isEmpty ? "未设置设备 IP" : device.deviceIP)
                            .font(.subheadline)
                            .foregroundStyle(.secondary)
                    }
                }
            }

            Section("端口映射") {
                if viewModel.mappings.isEmpty {
                    Text("暂无映射")
                        .foregroundStyle(.secondary)
                } else {
                    ForEach(viewModel.mappings) { mapping in
                        VStack(alignment: .leading, spacing: 8) {
                            HStack {
                                Text("\(mapping.localPort) → \(mapping.remotePort)")
                                    .font(.headline)
                                Spacer()
                                Text(mapping.enabled ? "启用" : "禁用")
                                    .font(.caption)
                                    .padding(.horizontal, 8)
                                    .padding(.vertical, 4)
                                    .background((mapping.enabled ? Color.blue : Color.gray).opacity(0.14), in: Capsule())
                            }

                            if !mapping.domain.isEmpty {
                                Text("域名：\(mapping.domain)")
                                    .font(.footnote)
                                    .foregroundStyle(.secondary)
                            }

                            HStack {
                                Spacer()
                                Button("删除", role: .destructive) {
                                    Task {
                                        await viewModel.deleteMapping(mapping)
                                    }
                                }
                                .buttonStyle(.bordered)
                            }
                        }
                        .padding(.vertical, 2)
                    }
                }
            }

            Section {
                Button {
                    showAddMappingPage = true
                } label: {
                    Label("新增映射", systemImage: "point.3.connected.trianglepath.dotted")
                }
            }
        }
        .navigationTitle("映射管理")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button {
                    Task {
                        await viewModel.reloadMappings()
                    }
                } label: {
                    Image(systemName: "arrow.clockwise")
                }
            }
        }
        .sheet(isPresented: $showAddMappingPage) {
            NavigationStack {
                FRPAddMappingView(viewModel: viewModel)
            }
        }
    }
}

private struct FRPAddMappingView: View {
    @ObservedObject var viewModel: FRPSettingsViewModel
    @Environment(\.dismiss) private var dismiss

    @State private var form: FRPCreateMappingInput = FRPCreateMappingInput()
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

            Section("端口参数") {
                TextField("本地端口（必填）", text: $form.localPort)
                    .keyboardType(.numberPad)
                TextField("远程端口（可选，留空自动分配）", text: $form.remotePort)
                    .keyboardType(.numberPad)
                TextField("域名（可选）", text: $form.domain)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
            }

            Section {
                Button {
                    Task {
                        isSaving = true
                        let success = await viewModel.createMapping(input: form)
                        isSaving = false
                        if success {
                            dismiss()
                        }
                    }
                } label: {
                    HStack {
                        Text("创建映射")
                        Spacer()
                        if isSaving {
                            ProgressView()
                        }
                    }
                }
                .disabled(isSaving)
            }
        }
        .navigationTitle("新增映射")
        .navigationBarTitleDisplayMode(.inline)
        .onAppear {
            if form.deviceID == nil {
                form.deviceID = viewModel.selectedDevice?.id ?? viewModel.devices.first?.id
            }
        }
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
