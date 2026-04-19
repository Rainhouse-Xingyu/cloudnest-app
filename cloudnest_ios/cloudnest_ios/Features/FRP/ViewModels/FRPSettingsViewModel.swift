import Foundation
import Combine

@MainActor
final class FRPSettingsViewModel: ObservableObject {
    @Published var devices: [FRPDeviceStatusItem] = []
    @Published var mappings: [FRPMappingItem] = []
    @Published var selectedDevice: FRPDeviceStatusItem?
    @Published var isLoading: Bool = false
    @Published var errorMessage: String? = nil
    @Published var saveMessage: String? = nil

    private let service: FRPServiceProtocol

    init(service: FRPServiceProtocol = FRPService()) {
        self.service = service
    }

    func onAppear() {
        Task {
            await reload()
        }
    }

    func reload() async {
        isLoading = true
        errorMessage = nil
        defer { isLoading = false }

        do {
            devices = try await service.loadDevicesWithStatus()
            if selectedDevice == nil {
                selectedDevice = devices.first
            } else if let currentID = selectedDevice?.id {
                selectedDevice = devices.first(where: { $0.id == currentID }) ?? devices.first
            }
            if let deviceID = selectedDevice?.id {
                mappings = try await service.loadMappings(deviceID: deviceID)
            } else {
                mappings = []
            }
        } catch {
            errorMessage = "加载 FRP 设备失败：\(error.localizedDescription)"
        }
    }

    func selectDevice(_ device: FRPDeviceStatusItem) async {
        selectedDevice = device
        await reloadMappings()
    }

    func reloadMappings() async {
        guard let deviceID = selectedDevice?.id else {
            mappings = []
            return
        }
        isLoading = true
        errorMessage = nil
        defer { isLoading = false }

        do {
            mappings = try await service.loadMappings(deviceID: deviceID)
        } catch {
            errorMessage = "加载映射失败：\(error.localizedDescription)"
        }
    }

    func createDevice(input: FRPCreateDeviceInput) async -> Bool {
        errorMessage = nil
        saveMessage = nil

        if input.deviceName.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            errorMessage = "请填写设备名称"
            return false
        }

        do {
            try await service.createDevice(input: input)
            saveMessage = "设备创建成功"
            await reload()
            return true
        } catch {
            errorMessage = "设备创建失败：\(error.localizedDescription)"
            return false
        }
    }

    func updateDeviceStatus(device: FRPDeviceStatusItem, enabled: Bool) async {
        errorMessage = nil
        saveMessage = nil

        do {
            try await service.updateDeviceStatus(deviceID: device.id, enabled: enabled)
            saveMessage = enabled ? "设备已启用" : "设备已禁用"
            await reload()
        } catch {
            errorMessage = "更新设备状态失败：\(error.localizedDescription)"
        }
    }

    func deleteDevice(device: FRPDeviceStatusItem) async {
        errorMessage = nil
        saveMessage = nil

        do {
            try await service.deleteDevice(deviceID: device.id)
            saveMessage = "设备已删除"
            if selectedDevice?.id == device.id {
                selectedDevice = nil
            }
            await reload()
        } catch {
            errorMessage = "删除设备失败：\(error.localizedDescription)"
        }
    }

    func createMapping(input: FRPCreateMappingInput) async -> Bool {
        errorMessage = nil
        saveMessage = nil

        guard input.deviceID != nil else {
            errorMessage = "请先选择设备"
            return false
        }
        guard let local = Int(input.localPort.trimmingCharacters(in: .whitespacesAndNewlines)), (1...65535).contains(local) else {
            errorMessage = "请填写有效的本地端口"
            return false
        }
        let remoteText = input.remotePort.trimmingCharacters(in: .whitespacesAndNewlines)
        if !remoteText.isEmpty {
            guard let remote = Int(remoteText), (1...65535).contains(remote) else {
                errorMessage = "请填写有效的远程端口"
                return false
            }
        }

        do {
            try await service.createMapping(input: input)
            saveMessage = "映射创建成功"
            await reloadMappings()
            return true
        } catch {
            errorMessage = "映射创建失败：\(error.localizedDescription)"
            return false
        }
    }

    func deleteMapping(_ mapping: FRPMappingItem) async {
        errorMessage = nil
        saveMessage = nil
        do {
            try await service.deleteMapping(mappingID: mapping.id)
            saveMessage = "映射已删除"
            await reloadMappings()
        } catch {
            errorMessage = "删除映射失败：\(error.localizedDescription)"
        }
    }

    func saveConfig(input: FRPConfigFormInput) async -> Bool {
        errorMessage = nil
        saveMessage = nil

        let cleanedAddr = input.serverAddr.trimmingCharacters(in: .whitespacesAndNewlines)
        if cleanedAddr.isEmpty {
            errorMessage = "请填写 server_addr"
            return false
        }
        if input.deviceID == nil {
            errorMessage = "请选择设备"
            return false
        }

        do {
            let result = try await service.saveConfigAndGenerateYAML(input: input)
            saveMessage = "保存成功，已生成 frpc.yaml：\(result.localFileURL.lastPathComponent)"
            await reload()
            return true
        } catch {
            errorMessage = "保存失败：\(error.localizedDescription)"
            return false
        }
    }
}
