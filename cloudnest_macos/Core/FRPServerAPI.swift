import Foundation

// Public server metadata never contains a token or encrypted credential.
struct FRPServer: Decodable, Identifiable, Sendable {
    let id: Int64
    let userID: Int64
    let revisionID: Int64
    let name: String
    let address: String
    let port: Int
    let tcpStart: Int
    let tcpEnd: Int
    let httpPort: Int
    let httpsPort: Int
    let enabled: Bool
    let tokenConfigured: Bool
    enum CodingKeys: String, CodingKey {
        case id, name, enabled
        case userID = "user_id", revisionID = "revision_id", address = "server_addr", port = "server_port"
        case tcpStart = "tcp_start", tcpEnd = "tcp_end", httpPort = "http_port", httpsPort = "https_port"
        case tokenConfigured = "token_configured"
    }

    func validate() throws {
        guard id > 0, userID > 0, revisionID > 0, !name.isEmpty, name.utf8.count <= 400,
              !address.isEmpty, address.utf8.count <= 253, (1...65535).contains(port),
              (1...65535).contains(tcpStart), (tcpStart...65535).contains(tcpEnd),
              (0...65535).contains(httpPort), (0...65535).contains(httpsPort) else { throw ServerError.invalidResponse }
    }
}

struct FRPServerPage: Sendable {
    let items: [FRPServer]
    let nextAfterID: Int64
}

struct FRPServerInput: Encodable, Sendable {
    let name: String
    let server_addr: String
    let server_port: Int
    let tcp_start: Int
    let tcp_end: Int
    let http_port: Int
    let https_port: Int
    let enabled: Bool
    let expected_revision_id: Int64
    // nil means retain the current secret. Empty string explicitly clears it.
    let token: String?

    func validate(updating: Bool) throws {
        guard !name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
              name.utf8.count <= 400, name.unicodeScalars.count <= 100,
              !server_addr.isEmpty, server_addr.utf8.count <= 253,
              (1...65535).contains(server_port), (1...65535).contains(tcp_start),
              (tcp_start...65535).contains(tcp_end), (0...65535).contains(http_port),
              (0...65535).contains(https_port), updating ? expected_revision_id > 0 : expected_revision_id == 0,
              token.map({ $0.utf8.count <= 1024 }) ?? true else { throw ServerError.invalidOperation }
        for scalar in [name, server_addr] + (token.map { [$0] } ?? []) {
            guard scalar.rangeOfCharacter(from: .controlCharacters) == nil,
                  !scalar.contains("{{"), !scalar.contains("}}") else { throw ServerError.invalidOperation }
        }
        let used = [server_port, http_port, https_port].filter { $0 > 0 }
        guard Set(used).count == used.count, !used.contains(where: { (tcp_start...tcp_end).contains($0) }) else {
            throw ServerError.invalidOperation
        }
    }
}

extension ServerAPI {
    func frpServers(_ origin: ServerOrigin, token: String, afterID: Int64) async throws -> FRPServerPage {
        struct Response: Decodable { let items: [FRPServer]; let next_after_id: Int64 }
        guard afterID >= 0 else { throw ServerError.invalidOperation }
        let result: Response = try await request(origin, "api/frp/servers", token: token,
            query: [URLQueryItem(name: "after_id", value: String(afterID)), URLQueryItem(name: "limit", value: "50")],
            maximumBytes: 512 * 1024)
        guard result.items.count <= 50, Set(result.items.map(\.id)).count == result.items.count,
              result.items.allSatisfy({ $0.id > afterID }),
              zip(result.items, result.items.dropFirst()).allSatisfy({ $0.id < $1.id }),
              result.next_after_id == (result.items.last?.id ?? 0) else { throw ServerError.invalidResponse }
        for item in result.items { try item.validate() }
        return FRPServerPage(items: result.items, nextAfterID: result.next_after_id)
    }

    func frpServer(_ origin: ServerOrigin, token: String, id: Int64) async throws -> FRPServer {
        struct Response: Decodable { let item: FRPServer }
        guard id > 0 else { throw ServerError.invalidOperation }
        let result: Response = try await request(origin, "api/frp/servers/\(id)", token: token, maximumBytes: 64 * 1024)
        try result.item.validate()
        guard result.item.id == id else { throw ServerError.invalidResponse }
        return result.item
    }

    func writeFRPServer(_ origin: ServerOrigin, token: String, id: Int64?, input: FRPServerInput) async throws -> FRPServer {
        struct Response: Decodable { let item: FRPServer }
        guard id.map({ $0 > 0 }) ?? true else { throw ServerError.invalidOperation }
        try input.validate(updating: id != nil)
        let body = try JSONEncoder().encode(input)
        guard body.count <= 4096 else { throw ServerError.invalidOperation }
        let result: Response = try await request(origin, id.map { "api/frp/servers/\($0)" } ?? "api/frp/servers",
            token: token, body: body, method: id == nil ? "POST" : "PUT", maximumBytes: 64 * 1024)
        try result.item.validate()
        guard id.map({ $0 == result.item.id }) ?? true,
              result.item.revisionID != input.expected_revision_id else { throw ServerError.invalidResponse }
        return result.item
    }
}
