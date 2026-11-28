import Foundation

struct FRPConfigSelection: Encodable, Equatable, Sendable {
    let device_id: Int64
    let server_id: Int64
    let server_revision_id: Int64

    func validate() throws {
        guard device_id > 0, server_id > 0, server_revision_id > 0 else { throw ServerError.invalidOperation }
    }
}

extension ServerAPI {
    func previewFRPConfig(_ origin: ServerOrigin, token: String, selection: FRPConfigSelection) async throws -> String {
        struct Preview: Decodable { let device_id: Int64; let config_text: String; let config_text_redacted: Bool }
        try selection.validate()
        let result: Preview = try await request(origin, "api/frp/configs/preview", token: token,
            body: try JSONEncoder().encode(selection), method: "POST", maximumBytes: 256 * 1024)
        guard result.device_id == selection.device_id, result.config_text_redacted,
              !result.config_text.isEmpty, result.config_text.utf8.count <= 128 * 1024 else { throw ServerError.invalidResponse }
        return result.config_text
    }

    func saveFRPConfig(_ origin: ServerOrigin, token: String, selection: FRPConfigSelection) async throws -> FRPConfig {
        struct Response: Decodable { let item: FRPConfig }
        try selection.validate()
        let result: Response = try await request(origin, "api/frp/configs/save", token: token,
            body: try JSONEncoder().encode(selection), method: "POST", maximumBytes: 64 * 1024)
        guard result.item.id > 0, result.item.deviceID == selection.device_id,
              result.item.serverID == selection.server_id, result.item.serverRevisionID == selection.server_revision_id else {
            throw ServerError.invalidResponse
        }
        return result.item
    }

    func rollbackFRPConfig(_ origin: ServerOrigin, token: String, configID: Int64, deviceID: Int64) async throws -> FRPConfig {
        struct Response: Decodable { let item: FRPConfig }
        guard configID > 0, deviceID > 0 else { throw ServerError.invalidOperation }
        let result: Response = try await request(origin, "api/frp/configs/\(configID)/rollback", token: token,
            body: Data("{}".utf8), method: "POST", maximumBytes: 64 * 1024)
        guard result.item.id > 0, result.item.id != configID, result.item.deviceID == deviceID,
              result.item.serverID >= 0, result.item.serverRevisionID >= 0 else { throw ServerError.invalidResponse }
        return result.item
    }
}
