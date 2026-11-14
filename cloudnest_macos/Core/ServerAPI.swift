import Foundation

struct ServerOrigin: Equatable, Sendable {
    let url: URL

    init(_ input: String) throws {
        guard var parts = URLComponents(string: input.trimmingCharacters(in: .whitespacesAndNewlines)),
              parts.scheme?.lowercased() == "https",
              let host = parts.host, !host.isEmpty,
              parts.user == nil, parts.password == nil, parts.query == nil, parts.fragment == nil,
              parts.percentEncodedPath.isEmpty || parts.percentEncodedPath == "/",
              parts.port.map({ (1...65535).contains($0) }) ?? true else {
            throw ServerError.invalidAddress
        }
        parts.scheme = "https"
        parts.host = host.lowercased()
        if parts.port == 443 { parts.port = nil }
        parts.path = ""
        guard let url = parts.url else { throw ServerError.invalidAddress }
        self.url = url
    }

    func endpoint(_ path: String) -> URL {
        url.appending(path: path)
    }
}

enum ServerError: LocalizedError, Equatable {
    case invalidAddress, invalidResponse, signInRejected, unauthorized, forbidden, rateLimited, unavailable

    var errorDescription: String? {
        switch self {
        case .invalidAddress: "请输入 HTTPS 服务器地址，可包含端口。"
        case .invalidResponse: "服务器返回了无法识别的响应。"
        case .signInRejected: "用户名或密码有误。"
        case .unauthorized: "会话已失效，请重新登录。"
        case .forbidden: "当前账户没有权限执行此操作。"
        case .rateLimited: "请求过于频繁，请稍后再试。"
        case .unavailable: "服务器暂时不可用，请检查连接后重试。"
        }
    }
}

struct Account: Decodable, Equatable, Sendable {
    let id: Int64
    let username: String
    let email: String?
    let isAdmin: Bool

    enum CodingKeys: String, CodingKey {
        case id, username, email
        case isAdmin = "is_admin"
    }
}

struct LoginResponse: Decodable, Sendable {
    let token: String
    let user: Account
    let tokenType: String

    enum CodingKeys: String, CodingKey {
        case token, user
        case tokenType = "token_type"
    }
}

private final class NoRedirect: NSObject, URLSessionTaskDelegate {
    func urlSession(_ session: URLSession, task: URLSessionTask,
                    willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest) async -> URLRequest? {
        nil
    }
}

struct ServerAPI: Sendable {
    private struct Credentials: Encodable { let username: String; let password: String }
    private struct Profile: Decodable { let user: Account }
    private struct Message: Decodable { let message: String }
    private let session: URLSession

    init(configuration: URLSessionConfiguration = .ephemeral) {
        configuration.httpCookieStorage = nil
        configuration.urlCredentialStorage = nil
        configuration.urlCache = nil
        configuration.timeoutIntervalForRequest = 15
        configuration.timeoutIntervalForResource = 30
        session = URLSession(configuration: configuration, delegate: NoRedirect(), delegateQueue: nil)
    }

    func login(_ origin: ServerOrigin, username: String, password: String) async throws -> LoginResponse {
        let body = try JSONEncoder().encode(Credentials(username: username, password: password))
        let result: LoginResponse = try await request(origin, "api/auth/login", body: body)
        guard result.tokenType.caseInsensitiveCompare("Bearer") == .orderedSame,
              !result.token.isEmpty, result.token.utf8.count <= 8192,
              result.token.utf8.allSatisfy({ (33...126).contains($0) }),
              result.user.id > 0, !result.user.username.isEmpty else { throw ServerError.invalidResponse }
        return result
    }

    func profile(_ origin: ServerOrigin, token: String) async throws -> Account {
        let result: Profile = try await request(origin, "api/auth/me", token: token)
        guard result.user.id > 0, !result.user.username.isEmpty else { throw ServerError.invalidResponse }
        return result.user
    }

    func logout(_ origin: ServerOrigin, token: String) async throws {
        let _: Message = try await request(origin, "api/auth/logout", token: token, method: "POST")
    }

    func health(_ origin: ServerOrigin, token: String) async throws {
        let _: Message = try await request(origin, "api/resource/health", token: token)
    }

    private func request<T: Decodable>(_ origin: ServerOrigin, _ path: String, token: String? = nil,
                                      body: Data? = nil, method: String = "GET") async throws -> T {
        var req = URLRequest(url: origin.endpoint(path), cachePolicy: .reloadIgnoringLocalCacheData)
        req.httpMethod = body == nil ? method : "POST"
        req.setValue("application/json", forHTTPHeaderField: "Accept")
        if let token { req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization") }
        if let body {
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
            req.httpBody = body
        }
        let (bytes, response) = try await session.bytes(for: req)
        guard let response = response as? HTTPURLResponse else { throw ServerError.invalidResponse }
        switch response.statusCode {
        case 200: break
        case 401: throw path == "api/auth/login" ? ServerError.signInRejected : ServerError.unauthorized
        case 403: throw ServerError.forbidden
        case 429: throw ServerError.rateLimited
        case 500...599: throw ServerError.unavailable
        default: throw ServerError.invalidResponse
        }
        var data = Data()
        for try await byte in bytes {
            guard data.count < 64 * 1024 else { throw ServerError.invalidResponse }
            data.append(byte)
        }
        do {
            return try JSONDecoder().decode(T.self, from: data)
        } catch {
            throw ServerError.invalidResponse
        }
    }
}
