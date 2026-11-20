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
    case invalidOperation, notFound, conflict, capacityExceeded

    var errorDescription: String? {
        switch self {
        case .invalidAddress: "请输入 HTTPS 服务器地址，可包含端口。"
        case .invalidResponse: "服务器返回了无法识别的响应。"
        case .signInRejected: "用户名或密码有误。"
        case .unauthorized: "会话已失效，请重新登录。"
        case .forbidden: "当前账户没有权限执行此操作。"
        case .rateLimited: "请求过于频繁，请稍后再试。"
        case .invalidOperation: "操作参数无效，请检查名称及目标目录。"
        case .notFound: "项目已被移动或删除，请刷新目录。"
        case .conflict: "项目已发生变化或目标中已有同名项目，请刷新后重试。"
        case .capacityExceeded: "服务器存储空间或传输额度不足。"
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
    private let transferSession: URLSession
    private let requestDelegate: any URLSessionTaskDelegate

    init(configuration: URLSessionConfiguration = .ephemeral) {
        configuration.httpCookieStorage = nil
        configuration.urlCredentialStorage = nil
        configuration.urlCache = nil
        configuration.timeoutIntervalForRequest = 15
        configuration.timeoutIntervalForResource = 30
        let delegate = NoRedirect()
        requestDelegate = delegate
        session = URLSession(configuration: configuration, delegate: delegate, delegateQueue: nil)
        let transferConfiguration = configuration.copy() as! URLSessionConfiguration
        transferConfiguration.timeoutIntervalForRequest = 90
        transferConfiguration.timeoutIntervalForResource = 24 * 60 * 60
        transferSession = URLSession(configuration: transferConfiguration, delegate: delegate, delegateQueue: nil)
    }

    init(session: URLSession, delegate: any URLSessionTaskDelegate) {
        self.session = session
        transferSession = session
        requestDelegate = delegate
    }

    func login(_ origin: ServerOrigin, username: String, password: String) async throws -> LoginResponse {
        let body = try JSONEncoder().encode(Credentials(username: username, password: password))
        let result: LoginResponse = try await request(origin, "api/auth/login", body: body, method: "POST")
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

    func request<T: Decodable>(_ origin: ServerOrigin, _ path: String, token: String? = nil,
                                      body: Data? = nil, method: String = "GET",
                                      query: [URLQueryItem] = [], maximumBytes: Int = 64 * 1024,
                                      headers: [String: String] = [:], contentType: String = "application/json",
                                      transfer: Bool = false) async throws -> T {
        let url = origin.endpoint(path).appending(queryItems: query)
        var req = URLRequest(url: url, cachePolicy: .reloadIgnoringLocalCacheData)
        req.httpMethod = method
        req.setValue("application/json", forHTTPHeaderField: "Accept")
        if let token { req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization") }
        if let body {
            req.setValue(contentType, forHTTPHeaderField: "Content-Type")
            req.httpBody = body
        }
        for (name, value) in headers { req.setValue(value, forHTTPHeaderField: name) }
        let transport = transfer ? transferSession : session
        let (bytes, response) = try await transport.bytes(for: req, delegate: requestDelegate)
        defer { bytes.task.cancel() }
        guard let response = response as? HTTPURLResponse else { throw ServerError.invalidResponse }
        try validateStatus(response.statusCode, login: path == "api/auth/login")
        var data = Data()
        for try await byte in bytes {
            guard data.count < maximumBytes else { throw ServerError.invalidResponse }
            data.append(byte)
        }
        do {
            return try JSONDecoder().decode(T.self, from: data)
        } catch {
            throw ServerError.invalidResponse
        }
    }
    func downloadResponse(_ origin: ServerOrigin, token: String, fileID: Int64, archive: Bool,
                          start: Int64?, end: Int64?, etag: String?) async throws -> (URLSession.AsyncBytes, HTTPURLResponse) {
        let path = archive ? "api/files/\(fileID)/download-zip" : "api/files/\(fileID)/download"
        var request = URLRequest(url: origin.endpoint(path), cachePolicy: .reloadIgnoringLocalCacheData)
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        request.setValue("identity", forHTTPHeaderField: "Accept-Encoding")
        if let start, let end {
            request.setValue("bytes=\(start)-\(end)", forHTTPHeaderField: "Range")
            if let etag { request.setValue(etag, forHTTPHeaderField: "If-Range") }
        }
        let (bytes, response) = try await transferSession.bytes(for: request, delegate: requestDelegate)
        guard let response = response as? HTTPURLResponse else { throw ServerError.invalidResponse }
        if ![200, 206, 416].contains(response.statusCode) { try validateStatus(response.statusCode) }
        return (bytes, response)
    }

    private func validateStatus(_ status: Int, login: Bool = false) throws {
        switch status {
        case 200: return
        case 400, 413, 422: throw ServerError.invalidOperation
        case 404: throw ServerError.notFound
        case 409, 412: throw ServerError.conflict
        case 507: throw ServerError.capacityExceeded
        case 401: throw login ? ServerError.signInRejected : ServerError.unauthorized
        case 403: throw ServerError.forbidden
        case 429: throw ServerError.rateLimited
        case 500...599: throw ServerError.unavailable
        default: throw ServerError.invalidResponse
        }
    }

}
