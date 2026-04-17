import Foundation

/// 登录请求体
struct LoginRequestDTO: Encodable {
    let username: String
    let password: String
}

/// 登录成功返回的用户信息
struct LoginUserDTO: Decodable {
    let id: Int64
    let username: String
    let email: String
    let isAdmin: Bool

    enum CodingKeys: String, CodingKey {
        case id
        case username
        case email
        case isAdmin = "is_admin"
    }
}

/// 登录接口响应体
struct LoginResponseDTO: Decodable {
    let token: String
    let tokenType: String
    let expiresAt: String
    let user: LoginUserDTO
    let serverHosts: ServerHostsDTO?

    enum CodingKeys: String, CodingKey {
        case token
        case tokenType = "token_type"
        case expiresAt = "expires_at"
        case user
        case serverHosts = "server_hosts"
    }
}

/// 登录返回的服务端地址候选信息（用于登录后自动切换局域网地址）。
struct ServerHostsDTO: Decodable {
    let requestHost: String?
    let lanIPs: [String]

    enum CodingKeys: String, CodingKey {
        case requestHost = "request_host"
        case lanIPs = "lan_ips"
    }
}

/// 登录会话模型（供页面展示与状态管理）
struct AuthSession {
    let token: String
    let tokenType: String
    let expiresAt: String
    let username: String
    let email: String
}
