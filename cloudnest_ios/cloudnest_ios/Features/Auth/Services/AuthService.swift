import Foundation

/// Auth 模块服务协议
protocol AuthServiceProtocol {
    func login(username: String, password: String, overrideBaseURL: String?) async throws -> LoginResponseDTO
}

/// Auth 模块服务默认实现
final class AuthService: AuthServiceProtocol {
    private let networkClient: NetworkClient

    init(networkClient: NetworkClient = AppContainer.shared.networkClient) {
        self.networkClient = networkClient
    }

    func login(username: String, password: String, overrideBaseURL: String? = nil) async throws -> LoginResponseDTO {
        let dto = LoginRequestDTO(username: username, password: password)
        let body = try JSONEncoder().encode(dto)
        let endpoint = AuthLoginEndpoint(body: body)

        // 勾选外网连接时，使用用户输入的地址构建临时客户端
        if let overrideBaseURL, !overrideBaseURL.isEmpty {
            let timeout = AppContainer.shared.environment.requestTimeout
            let temporaryClient = URLSessionNetworkClient(baseURL: overrideBaseURL, timeout: timeout)
            return try await temporaryClient.request(endpoint, decode: LoginResponseDTO.self)
        }

        return try await networkClient.request(endpoint, decode: LoginResponseDTO.self)
    }
}

/// 登录接口定义：POST /api/auth/login
private struct AuthLoginEndpoint: APIEndpoint {
    let body: Data?

    var path: String { "/api/auth/login" }
    var method: HTTPMethod { .post }
    var headers: [String: String] {
        [
            "Content-Type": "application/json",
            "Accept": "application/json"
        ]
    }
}
