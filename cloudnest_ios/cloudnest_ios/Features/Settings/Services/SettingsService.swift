import Foundation

/// Settings 模块服务协议
protocol SettingsServiceProtocol {
    func loadTitle() -> String
    func createManagedUser(_ request: CreateManagedUserRequestDTO) async throws -> CreateManagedUserResponseDTO
    func logout() async throws
}

/// Settings 模块服务默认实现
final class SettingsService: SettingsServiceProtocol {
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

    func loadTitle() -> String {
        "设置"
    }

    func createManagedUser(_ request: CreateManagedUserRequestDTO) async throws -> CreateManagedUserResponseDTO {
        let body = try JSONEncoder().encode(request)
        let endpoint = AdminCreateUserEndpoint(body: body, token: try token)
        return try await networkClient.request(endpoint, decode: CreateManagedUserResponseDTO.self)
    }

    func logout() async throws {
        let endpoint = AuthLogoutEndpoint(token: try token)
        _ = try await networkClient.request(endpoint)
    }
}

private struct AdminCreateUserEndpoint: APIEndpoint {
    let body: Data?
    let token: String

    var path: String { "/api/admin/users" }
    var method: HTTPMethod { .post }
    var headers: [String: String] {
        [
            "Authorization": "Bearer \(token)",
            "Content-Type": "application/json",
            "Accept": "application/json"
        ]
    }
}

private struct AuthLogoutEndpoint: APIEndpoint {
    let token: String

    var path: String { "/api/auth/logout" }
    var method: HTTPMethod { .post }
    var headers: [String: String] {
        [
            "Authorization": "Bearer \(token)",
            "Accept": "application/json"
        ]
    }
}
