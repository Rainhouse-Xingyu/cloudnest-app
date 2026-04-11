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

    enum CodingKeys: String, CodingKey {
        case token
        case tokenType = "token_type"
        case expiresAt = "expires_at"
        case user
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
