import Foundation
import Combine

@MainActor
final class FRPSettingsViewModel: ObservableObject {
    @Published var devices: [FRPDeviceStatusItem] = []
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
        } catch {
            errorMessage = "加载 FRP 设备失败：\(error.localizedDescription)"
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
