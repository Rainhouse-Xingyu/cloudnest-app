import Foundation

struct FRPMappingSnapshot: Encodable, Sendable {
    let device_id: Int64
    let server_id: Int64
    let local_port: Int
    let remote_port: Int
    let domain: String
    let status: Int
    init(_ item: FRPMapping) {
        device_id = item.deviceID; server_id = item.serverID; local_port = item.localPort
        remote_port = item.remotePort; domain = item.domain; status = item.status
    }
}

extension ServerAPI {
    func writeFRPMapping(_ origin: ServerOrigin, token: String, original: FRPMapping?, deviceID: Int64,
                         serverID: Int64, localPort: Int, remotePort: Int?, domain: String, enabled: Bool) async throws -> FRPMapping {
        struct Input: Encodable {
            let device_id: Int64?
            let server_id: Int64?
            let local_port: Int
            let remote_port: Int?
            let domain: String
            let status: Int
            let expected: FRPMappingSnapshot?
        }
        struct Response: Decodable { let item: FRPMapping }
        let cleanDomain = domain.trimmingCharacters(in: .whitespacesAndNewlines)
        let lowerDomain = cleanDomain.lowercased()
        let secure = lowerDomain.hasPrefix("https://")
        var host = secure ? String(lowerDomain.dropFirst(8)) : lowerDomain.hasPrefix("http://") ? String(lowerDomain.dropFirst(7)) : lowerDomain
        if host.hasSuffix(".") { host.removeLast() }
        let expectedDomain = secure ? "https://" + host : host
        guard deviceID > 0, serverID >= 0, (1...65535).contains(localPort),
              cleanDomain.utf8.count <= 400, cleanDomain.rangeOfCharacter(from: .controlCharacters) == nil,
              original.map({ $0.id > 0 && $0.deviceID == deviceID && $0.serverID == serverID }) ?? (serverID > 0) else {
            throw ServerError.invalidOperation
        }
        let modernDomain = serverID > 0 && !cleanDomain.isEmpty
        if modernDomain {
            guard remotePort == nil || remotePort == 0 else { throw ServerError.invalidOperation }
        } else if let remotePort {
            guard (1...65535).contains(remotePort) else { throw ServerError.invalidOperation }
        } else if let original {
            // Only a modern HTTP/S -> TCP transition supports allocation on
            // update. Omitting a TCP port on an existing TCP mapping keeps it.
            guard serverID > 0, !original.domain.isEmpty, cleanDomain.isEmpty else { throw ServerError.invalidOperation }
        }
        let input = Input(device_id: original == nil ? deviceID : nil, server_id: original == nil ? serverID : nil,
            local_port: localPort, remote_port: modernDomain ? 0 : remotePort, domain: cleanDomain, status: enabled ? 1 : 0,
            expected: original.map(FRPMappingSnapshot.init))
        let result: Response = try await request(origin, original.map { "api/frp/mappings/\($0.id)" } ?? "api/frp/mappings",
            token: token, body: try JSONEncoder().encode(input), method: original == nil ? "POST" : "PUT", maximumBytes: 64 * 1024)
        let item = result.item
        let domainMatches = item.domain == expectedDomain
        let portMatches = modernDomain ? item.remotePort == 0 : item.remotePort > 0
        let explicitPortMatches = modernDomain || (remotePort.map({ item.remotePort == $0 }) ?? true)
        guard item.id > 0, item.deviceID == deviceID, item.serverID == serverID,
              item.localPort == localPort, (0...65535).contains(item.remotePort), item.domain.utf8.count <= 400,
              item.status == (enabled ? 1 : 0), original.map({ item.id == $0.id }) ?? true,
              domainMatches, portMatches, explicitPortMatches else { throw ServerError.invalidResponse }
        return item
    }

    func deleteFRPMapping(_ origin: ServerOrigin, token: String, id: Int64) async throws {
        struct Response: Decodable { let message: String }
        guard id > 0 else { throw ServerError.invalidOperation }
        let result: Response = try await request(origin, "api/frp/mappings/\(id)", token: token, method: "DELETE", maximumBytes: 64 * 1024)
        guard result.message == "deleted" else { throw ServerError.invalidResponse }
    }
}
