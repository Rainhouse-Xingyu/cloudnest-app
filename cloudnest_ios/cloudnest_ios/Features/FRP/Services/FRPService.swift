import Foundation

protocol FRPServiceProtocol {
    func loadDevicesWithStatus() async throws -> [FRPDeviceStatusItem]
    func createDevice(input: FRPCreateDeviceInput) async throws
    func updateDeviceStatus(deviceID: Int64, enabled: Bool) async throws
    func deleteDevice(deviceID: Int64) async throws
    func loadMappings(deviceID: Int64) async throws -> [FRPMappingItem]
    func createMapping(input: FRPCreateMappingInput) async throws
    func deleteMapping(mappingID: Int64) async throws
    func saveConfigAndGenerateYAML(input: FRPConfigFormInput) async throws -> FRPYamlGenerateResult
}

final class FRPService: FRPServiceProtocol {
    private let networkClient: NetworkClient

    init(networkClient: NetworkClient = AppContainer.shared.networkClient) {
        self.networkClient = networkClient
    }

    private var token: String {
        get throws {
            guard let tk = SessionManager.shared.loadSession()?.token, !tk.isEmpty else {
                throw NetworkError.invalidResponse
            }
            return tk
        }
    }

    func loadDevicesWithStatus() async throws -> [FRPDeviceStatusItem] {
        let tk = try token
        let listResponse: FRPDeviceListResponseDTO = try await networkClient.request(
            FRPDeviceListEndpoint(token: tk),
            decode: FRPDeviceListResponseDTO.self
        )

        var statusMap: [Int64: FRPDeviceStatusDetailDTO] = [:]
        await withTaskGroup(of: (Int64, FRPDeviceStatusDetailDTO?).self) { group in
            for device in listResponse.items {
                group.addTask { [networkClient] in
                    do {
                        let response: FRPDeviceStatusResponseDTO = try await networkClient.request(
                            FRPDeviceStatusEndpoint(deviceID: device.id, token: tk),
                            decode: FRPDeviceStatusResponseDTO.self
                        )
                        return (device.id, response.item)
                    } catch {
                        return (device.id, nil)
                    }
                }
            }

            for await (deviceID, status) in group {
                if let status {
                    statusMap[deviceID] = status
                }
            }
        }

        return listResponse.items.map { device in
            let detail = statusMap[device.id]
            return FRPDeviceStatusItem(
                id: device.id,
                deviceName: device.deviceName,
                deviceIP: device.deviceIP,
                enabled: device.status == 1,
                online: detail?.online ?? false,
                lastHeartbeat: detail?.lastHeartbeat ?? "",
                clientVersion: detail?.clientVersion ?? ""
            )
        }
        .sorted { lhs, rhs in
            if lhs.online != rhs.online {
                return lhs.online && !rhs.online
            }
            return lhs.id > rhs.id
        }
    }

    func createDevice(input: FRPCreateDeviceInput) async throws {
        let payload = FRPCreateDeviceRequestDTO(
            deviceName: input.deviceName.trimmingCharacters(in: .whitespacesAndNewlines),
            deviceIP: input.deviceIP.trimmingCharacters(in: .whitespacesAndNewlines)
        )
        let body = try JSONEncoder().encode(payload)
        _ = try await networkClient.request(FRPCreateDeviceEndpoint(token: try token, body: body), decode: FRPSingleDeviceResponseDTO.self)
    }

    func updateDeviceStatus(deviceID: Int64, enabled: Bool) async throws {
        let payload = FRPUpdateDeviceStatusRequestDTO(status: enabled ? 1 : 0)
        let body = try JSONEncoder().encode(payload)
        _ = try await networkClient.request(
            FRPUpdateDeviceEndpoint(deviceID: deviceID, token: try token, body: body),
            decode: FRPSingleDeviceResponseDTO.self
        )
    }

    func deleteDevice(deviceID: Int64) async throws {
        _ = try await networkClient.request(FRPDeleteDeviceEndpoint(deviceID: deviceID, token: try token), decode: FRPMessageResponseDTO.self)
    }

    func loadMappings(deviceID: Int64) async throws -> [FRPMappingItem] {
        let response: FRPMappingListResponseDTO = try await networkClient.request(
            FRPMappingListEndpoint(deviceID: deviceID, token: try token),
            decode: FRPMappingListResponseDTO.self
        )
        return response.items.map {
            FRPMappingItem(
                id: $0.id,
                deviceID: $0.deviceID,
                localPort: $0.localPort,
                remotePort: $0.remotePort,
                domain: $0.domain,
                enabled: $0.status == 1
            )
        }
    }

    func createMapping(input: FRPCreateMappingInput) async throws {
        guard let deviceID = input.deviceID else {
            throw NetworkError.invalidResponse
        }
        guard let localPort = Int(input.localPort.trimmingCharacters(in: .whitespacesAndNewlines)), (1...65535).contains(localPort) else {
            throw NetworkError.invalidResponse
        }
        let cleanedRemote = input.remotePort.trimmingCharacters(in: .whitespacesAndNewlines)
        let remotePort = cleanedRemote.isEmpty ? nil : Int(cleanedRemote)
        if let remotePort, !(1...65535).contains(remotePort) {
            throw NetworkError.invalidResponse
        }

        let payload = FRPCreateMappingRequestDTO(
            deviceID: deviceID,
            localPort: localPort,
            remotePort: remotePort,
            domain: input.domain.trimmingCharacters(in: .whitespacesAndNewlines),
            status: 1
        )
        let body = try JSONEncoder().encode(payload)
        _ = try await networkClient.request(FRPCreateMappingEndpoint(token: try token, body: body), decode: FRPMappingSingleResponseDTO.self)
    }

    func deleteMapping(mappingID: Int64) async throws {
        _ = try await networkClient.request(FRPDeleteMappingEndpoint(mappingID: mappingID, token: try token), decode: FRPMessageResponseDTO.self)
    }

    func saveConfigAndGenerateYAML(input: FRPConfigFormInput) async throws -> FRPYamlGenerateResult {
        guard let deviceID = input.deviceID else {
            throw NetworkError.invalidResponse
        }

        guard let serverPort = Int(input.serverPort.trimmingCharacters(in: .whitespacesAndNewlines)) else {
            throw NetworkError.invalidResponse
        }

        let payload = FRPSaveConfigRequestDTO(
            deviceID: deviceID,
            serverAddr: input.serverAddr.trimmingCharacters(in: .whitespacesAndNewlines),
            serverPort: serverPort,
            token: input.token.trimmingCharacters(in: .whitespacesAndNewlines)
        )
        let body = try JSONEncoder().encode(payload)

        let response: FRPSaveConfigResponseDTO = try await networkClient.request(
            FRPSaveConfigEndpoint(token: try token, body: body),
            decode: FRPSaveConfigResponseDTO.self
        )

        let outputURL = try persistLatestYAML(content: response.item.configText, deviceID: deviceID)
        return FRPYamlGenerateResult(deviceID: deviceID, localFileURL: outputURL)
    }

    /// 保存后自动生成最新 frpc.yaml，供用户导出或后续上传设备。
    private func persistLatestYAML(content: String, deviceID: Int64) throws -> URL {
        let directory = try createConfigDirectoryIfNeeded()
        let deviceFileURL = directory.appendingPathComponent("frpc_\(deviceID).yaml")
        let latestFileURL = directory.appendingPathComponent("frpc.yaml")

        try content.write(to: deviceFileURL, atomically: true, encoding: .utf8)
        try content.write(to: latestFileURL, atomically: true, encoding: .utf8)

        return latestFileURL
    }

    private func createConfigDirectoryIfNeeded() throws -> URL {
        let baseDirectory = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask).first
            ?? FileManager.default.temporaryDirectory
        let frpDirectory = baseDirectory.appendingPathComponent("FRPConfigs", isDirectory: true)
        if !FileManager.default.fileExists(atPath: frpDirectory.path) {
            try FileManager.default.createDirectory(at: frpDirectory, withIntermediateDirectories: true)
        }
        return frpDirectory
    }
}

// MARK: - DTOs

private struct FRPDeviceListResponseDTO: Decodable {
    let items: [FRPDeviceListItemDTO]
}

private struct FRPDeviceListItemDTO: Decodable {
    let id: Int64
    let deviceName: String
    let deviceIP: String
    let status: Int8

    enum CodingKeys: String, CodingKey {
        case id
        case deviceName = "device_name"
        case deviceIP = "device_ip"
        case status
    }
}

private struct FRPDeviceStatusResponseDTO: Decodable {
    let item: FRPDeviceStatusDetailDTO
}

private struct FRPDeviceStatusDetailDTO: Decodable {
    let online: Bool
    let lastHeartbeat: String?
    let clientVersion: String?

    enum CodingKeys: String, CodingKey {
        case online
        case lastHeartbeat = "last_heartbeat"
        case clientVersion = "client_version"
    }
}

private struct FRPSaveConfigRequestDTO: Encodable {
    let deviceID: Int64
    let serverAddr: String
    let serverPort: Int
    let token: String

    enum CodingKeys: String, CodingKey {
        case deviceID = "device_id"
        case serverAddr = "server_addr"
        case serverPort = "server_port"
        case token
    }
}

private struct FRPCreateDeviceRequestDTO: Encodable {
    let deviceName: String
    let deviceIP: String

    enum CodingKeys: String, CodingKey {
        case deviceName = "device_name"
        case deviceIP = "device_ip"
    }
}

private struct FRPUpdateDeviceStatusRequestDTO: Encodable {
    let status: Int8
}

private struct FRPCreateMappingRequestDTO: Encodable {
    let deviceID: Int64
    let localPort: Int
    let remotePort: Int?
    let domain: String
    let status: Int8

    enum CodingKeys: String, CodingKey {
        case deviceID = "device_id"
        case localPort = "local_port"
        case remotePort = "remote_port"
        case domain
        case status
    }
}

private struct FRPMappingListResponseDTO: Decodable {
    let items: [FRPMappingItemDTO]
}

private struct FRPMappingSingleResponseDTO: Decodable {
    let item: FRPMappingItemDTO
}

private struct FRPSingleDeviceResponseDTO: Decodable {
    let item: FRPDeviceListItemDTO
}

private struct FRPMessageResponseDTO: Decodable {
    let message: String?
}

private struct FRPMappingItemDTO: Decodable {
    let id: Int64
    let deviceID: Int64
    let localPort: Int
    let remotePort: Int
    let domain: String
    let status: Int8

    enum CodingKeys: String, CodingKey {
        case id
        case deviceID = "device_id"
        case localPort = "local_port"
        case remotePort = "remote_port"
        case domain
        case status
    }
}

private struct FRPSaveConfigResponseDTO: Decodable {
    let item: FRPConfigItemDTO
}

private struct FRPConfigItemDTO: Decodable {
    let id: Int64
    let deviceID: Int64?
    let configText: String

    enum CodingKeys: String, CodingKey {
        case id
        case deviceID = "device_id"
        case configText = "config_text"
    }
}

// MARK: - Endpoints

private struct FRPDeviceListEndpoint: APIEndpoint {
    let token: String

    var path: String { "/api/frp/devices" }
    var method: HTTPMethod { .get }
    var headers: [String : String] {
        [
            "Authorization": "Bearer \(token)",
            "Accept": "application/json"
        ]
    }
}

private struct FRPDeviceStatusEndpoint: APIEndpoint {
    let deviceID: Int64
    let token: String

    var path: String { "/api/frp/devices/\(deviceID)/status" }
    var method: HTTPMethod { .get }
    var headers: [String : String] {
        [
            "Authorization": "Bearer \(token)",
            "Accept": "application/json"
        ]
    }
}

private struct FRPSaveConfigEndpoint: APIEndpoint {
    let token: String
    let body: Data?

    var path: String { "/api/frp/configs/save" }
    var method: HTTPMethod { .post }
    var headers: [String : String] {
        [
            "Authorization": "Bearer \(token)",
            "Content-Type": "application/json",
            "Accept": "application/json"
        ]
    }
}

private struct FRPCreateDeviceEndpoint: APIEndpoint {
    let token: String
    let body: Data?

    var path: String { "/api/frp/devices" }
    var method: HTTPMethod { .post }
    var headers: [String : String] {
        [
            "Authorization": "Bearer \(token)",
            "Content-Type": "application/json",
            "Accept": "application/json"
        ]
    }
}

private struct FRPUpdateDeviceEndpoint: APIEndpoint {
    let deviceID: Int64
    let token: String
    let body: Data?

    var path: String { "/api/frp/devices/\(deviceID)" }
    var method: HTTPMethod { .put }
    var headers: [String : String] {
        [
            "Authorization": "Bearer \(token)",
            "Content-Type": "application/json",
            "Accept": "application/json"
        ]
    }
}

private struct FRPDeleteDeviceEndpoint: APIEndpoint {
    let deviceID: Int64
    let token: String

    var path: String { "/api/frp/devices/\(deviceID)" }
    var method: HTTPMethod { .delete }
    var headers: [String : String] {
        [
            "Authorization": "Bearer \(token)",
            "Accept": "application/json"
        ]
    }
}

private struct FRPMappingListEndpoint: APIEndpoint {
    let deviceID: Int64
    let token: String

    var path: String { "/api/frp/devices/\(deviceID)/mappings" }
    var method: HTTPMethod { .get }
    var headers: [String : String] {
        [
            "Authorization": "Bearer \(token)",
            "Accept": "application/json"
        ]
    }
}

private struct FRPCreateMappingEndpoint: APIEndpoint {
    let token: String
    let body: Data?

    var path: String { "/api/frp/mappings" }
    var method: HTTPMethod { .post }
    var headers: [String : String] {
        [
            "Authorization": "Bearer \(token)",
            "Content-Type": "application/json",
            "Accept": "application/json"
        ]
    }
}

private struct FRPDeleteMappingEndpoint: APIEndpoint {
    let mappingID: Int64
    let token: String

    var path: String { "/api/frp/mappings/\(mappingID)" }
    var method: HTTPMethod { .delete }
    var headers: [String : String] {
        [
            "Authorization": "Bearer \(token)",
            "Accept": "application/json"
        ]
    }
}
