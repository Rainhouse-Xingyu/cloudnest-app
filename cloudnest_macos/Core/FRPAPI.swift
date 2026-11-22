import Foundation

struct FRPDevice: Decodable, Identifiable, Sendable {
    let id: Int64
    let userID: Int64
    let name: String
    let address: String
    let status: Int
    enum CodingKeys: String, CodingKey {
        case id, status
        case userID = "user_id", name = "device_name", address = "device_ip"
    }
}

struct FRPMapping: Decodable, Identifiable, Sendable {
    let id: Int64
    let deviceID: Int64
    let serverID: Int64
    let localPort: Int
    let remotePort: Int
    let domain: String
    let status: Int
    enum CodingKeys: String, CodingKey {
        case id, domain, status
        case deviceID = "device_id", serverID = "server_id", localPort = "local_port", remotePort = "remote_port"
    }
}

struct FRPConfig: Decodable, Identifiable, Sendable {
    let id: Int64
    let deviceID: Int64?
    let serverID: Int64
    let serverRevisionID: Int64
    let createdAt: String
    enum CodingKeys: String, CodingKey {
        case id
        case deviceID = "device_id", serverID = "server_id", serverRevisionID = "server_revision_id", createdAt = "create_time"
    }
}

struct FRPRuntime: Decodable, Sendable {
    let state: String
    let configID: Int64
    let observedAt: String
    let expected: Int
    let registered: Int
    let failed: Int
    let pending: Int
    enum CodingKeys: String, CodingKey {
        case state, expected, registered, failed, pending
        case configID = "config_id", observedAt = "observed_at"
    }
}

struct FRPProxyStatus: Decodable, Sendable {
    let mappingID: Int64?
    let state: String
    let upstream: String?
    enum CodingKeys: String, CodingKey {
        case state
        case mappingID = "mapping_id", upstream = "upstream_tcp"
    }
}

struct FRPStatus: Decodable, Sendable {
    let online: Bool
    let heartbeat: String?
    let lastError: String
    let clientVersion: String
    let runtime: FRPRuntime?
    let proxies: [FRPProxyStatus]?
    enum CodingKeys: String, CodingKey {
        case online, runtime
        case heartbeat = "last_heartbeat", lastError = "last_error", clientVersion = "client_version", proxies = "runtime_proxies"
    }
}

struct FRPCommand: Decodable, Identifiable, Sendable {
    let id: String
    let requestID: String
    let deviceID: Int64
    let action: String
    let configID: Int64
    let status: String
    let attempts: Int
    let resultCode: String
    let createdAt: String
    let completedAt: String?
    let diagnostic: FRPRuntime?
    enum CodingKeys: String, CodingKey {
        case action, status, attempts, diagnostic
        case id = "command_id", requestID = "request_id", deviceID = "device_id", configID = "config_id"
        case resultCode = "result_code", createdAt = "created_at", completedAt = "completed_at"
    }
}

extension ServerAPI {
    private struct FRPList<T: Decodable>: Decodable { let items: [T]? }
    private struct FRPItem<T: Decodable>: Decodable { let item: T }

    func enqueueFRPCommand(_ origin: ServerOrigin, token: String, intent: FRPCommandIntent) async throws -> FRPCommand {
        struct Input: Encodable { let request_id: String; let action: String; let config_id: Int64 }
        struct Receipt: Decodable { let item: FRPCommand; let command_id: String }
        try intent.validate()
        guard intent.outcome == .unconfirmed else { throw ServerError.invalidOperation }
        let body = try JSONEncoder().encode(Input(request_id: intent.requestID, action: intent.action.rawValue, config_id: intent.configID))
        let receipt: Receipt = try await request(origin, "api/frp/devices/\(intent.deviceID)/commands", token: token,
            body: body, method: "POST", maximumBytes: 512 * 1024)
        let item = receipt.item
        guard FRPCommandIntent.validID(item.id), receipt.command_id == item.id,
              item.requestID == intent.requestID, item.deviceID == intent.deviceID,
              item.action == intent.action.rawValue, item.configID == intent.configID, item.attempts >= 0,
              ["pending", "leased", "succeeded", "failed", "cancelled", "timed_out"].contains(item.status) else {
            throw ServerError.invalidResponse
        }
        return item
    }

    func frpDevices(_ origin: ServerOrigin, token: String) async throws -> [FRPDevice] {
        let result: FRPList<FRPDevice> = try await request(origin, "api/frp/devices", token: token, maximumBytes: 4 * 1024 * 1024)
        let items = result.items ?? []
        guard items.count <= 5000, Set(items.map(\.id)).count == items.count,
              items.allSatisfy({ $0.id > 0 && $0.userID > 0 && [0, 1].contains($0.status) }) else { throw ServerError.invalidResponse }
        return items
    }

    func frpMappings(_ origin: ServerOrigin, token: String, deviceID: Int64) async throws -> [FRPMapping] {
        guard deviceID > 0 else { throw ServerError.invalidOperation }
        let result: FRPList<FRPMapping> = try await request(origin, "api/frp/devices/\(deviceID)/mappings", token: token, maximumBytes: 2 * 1024 * 1024)
        let items = result.items ?? []
        guard items.count <= 5000, Set(items.map(\.id)).count == items.count,
              items.allSatisfy({ $0.id > 0 && $0.deviceID == deviceID && $0.serverID >= 0 &&
                  (1...65535).contains($0.localPort) && (0...65535).contains($0.remotePort) && [0, 1].contains($0.status) }) else {
            throw ServerError.invalidResponse
        }
        return items
    }

    func frpStatus(_ origin: ServerOrigin, token: String, deviceID: Int64) async throws -> FRPStatus {
        guard deviceID > 0 else { throw ServerError.invalidOperation }
        let result: FRPItem<FRPStatus> = try await request(origin, "api/frp/devices/\(deviceID)/status", token: token, maximumBytes: 256 * 1024)
        return result.item
    }

    func frpConfigs(_ origin: ServerOrigin, token: String, deviceID: Int64) async throws -> [FRPConfig] {
        guard deviceID > 0 else { throw ServerError.invalidOperation }
        let result: FRPList<FRPConfig> = try await request(origin, "api/frp/devices/\(deviceID)/configs", token: token, maximumBytes: 2 * 1024 * 1024)
        let items = result.items ?? []
        guard items.count <= 5000, Set(items.map(\.id)).count == items.count,
              items.allSatisfy({ $0.id > 0 && $0.deviceID == deviceID && $0.serverID >= 0 && $0.serverRevisionID >= 0 }) else {
            throw ServerError.invalidResponse
        }
        return items
    }

    func frpCommands(_ origin: ServerOrigin, token: String, deviceID: Int64) async throws -> [FRPCommand] {
        guard deviceID > 0 else { throw ServerError.invalidOperation }
        let result: FRPList<FRPCommand> = try await request(origin, "api/frp/devices/\(deviceID)/commands", token: token,
            query: [URLQueryItem(name: "limit", value: "20")], maximumBytes: 512 * 1024)
        let items = result.items ?? []
        guard items.count <= 20, Set(items.map(\.id)).count == items.count,
              items.allSatisfy({ !$0.id.isEmpty && $0.deviceID == deviceID && $0.configID >= 0 && $0.attempts >= 0 }) else {
            throw ServerError.invalidResponse
        }
        return items
    }
}
