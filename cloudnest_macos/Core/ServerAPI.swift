import Foundation
import CryptoKit

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
    case invalidOperation, notFound, conflict, capacityExceeded, sessionChanged
    case reauthenticationRejected

    var errorDescription: String? {
        switch self {
        case .invalidAddress: "请输入 HTTPS 服务器地址，可包含端口。"
        case .invalidResponse: "服务器返回了无法识别的响应。"
        case .signInRejected: "用户名或密码有误。"
        case .reauthenticationRejected: "当前密码验证未通过，请重新输入。"
        case .unauthorized: "会话已失效，请重新登录。"
        case .sessionChanged: "账户或服务器已切换，请重新选择文件或操作。"
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

private final class AnonymousTLSPolicy: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
    let policy: any URLSessionTaskDelegate
    init(policy: any URLSessionTaskDelegate) { self.policy = policy }
    func urlSession(_ session: URLSession, task: URLSessionTask, didReceive challenge: URLAuthenticationChallenge,
                    completionHandler: @escaping @Sendable (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust else {
            completionHandler(.cancelAuthenticationChallenge, nil); return
        }
        policy.urlSession?(session, task: task, didReceive: challenge, completionHandler: completionHandler)
            ?? completionHandler(.performDefaultHandling, nil)
    }
}

struct ServerAPI: Sendable {
    struct EmptyResponse: Decodable, Sendable {}
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
                                      transfer: Bool = false, telemetry: TransferTelemetry? = nil,
                                      successStatus: Int = 200) async throws -> T {
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
        let delegate: any URLSessionTaskDelegate
        if let telemetry { delegate = UploadProgressDelegate(policy: requestDelegate, telemetry: telemetry) }
        else { delegate = requestDelegate }
        let (bytes, response) = try await transport.bytes(for: req, delegate: delegate)
        defer { bytes.task.cancel() }
        guard let response = response as? HTTPURLResponse else { throw ServerError.invalidResponse }
        try validateStatus(response.statusCode, login: path == "api/auth/login", successStatus: successStatus)
        var data = Data()
        for try await byte in bytes {
            guard data.count < maximumBytes else { throw ServerError.invalidResponse }
            data.append(byte)
        }
        if successStatus == 204 {
            guard data.isEmpty, let value = EmptyResponse() as? T else { throw ServerError.invalidResponse }
            return value
        }
        do {
            return try JSONDecoder().decode(T.self, from: data)
        } catch {
            throw ServerError.invalidResponse
        }
    }
    func photoThumbnail(_ origin: ServerOrigin, token: String, fileID: Int64) async throws -> Data {
        guard fileID > 0 else { throw ServerError.invalidOperation }
        let url = origin.endpoint("api/files/\(fileID)/thumbnail").appending(queryItems:[URLQueryItem(name:"size",value:"256")])
        var request = URLRequest(url:url,cachePolicy:.reloadIgnoringLocalCacheData)
        request.setValue("Bearer \(token)",forHTTPHeaderField:"Authorization")
        request.setValue("image/jpeg",forHTTPHeaderField:"Accept")
        request.setValue("identity",forHTTPHeaderField:"Accept-Encoding")
        let (bytes,response) = try await session.bytes(for:request,delegate:requestDelegate)
        defer { bytes.task.cancel() }
        guard let response = response as? HTTPURLResponse else { throw ServerError.invalidResponse }
        try validateStatus(response.statusCode,login:false,successStatus:200)
        let identity = try DownloadIdentity(response:response)
        guard identity.length > 0, identity.length <= 512*1024,
              response.value(forHTTPHeaderField:"Content-Type")?.lowercased() == "image/jpeg" else { throw ServerError.invalidResponse }
        var data = Data()
        for try await byte in bytes {
            try Task.checkCancellation()
            guard data.count < identity.length else { throw ServerError.invalidResponse }
            data.append(byte)
        }
        guard data.count == identity.length, TransferDisk.hex(SHA256.hash(data:data)) == identity.sha256 else { throw TransferError.integrity }
        return data
    }

    func downloadResponse(_ origin: ServerOrigin, token: String, fileID: Int64, archive: Bool,
                          version: Int? = nil, start: Int64?, end: Int64?, etag: String?) async throws -> (DownloadStream, HTTPURLResponse) {
        guard fileID > 0, version.map({ $0 > 0 && !archive }) ?? true else { throw ServerError.invalidOperation }
        let path: String
        if let version { path = "api/files/\(fileID)/versions/\(version)/download" }
        else { path = archive ? "api/files/\(fileID)/download-zip" : "api/files/\(fileID)/download" }
        var request = URLRequest(url: origin.endpoint(path), cachePolicy: .reloadIgnoringLocalCacheData)
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        request.setValue("identity", forHTTPHeaderField: "Accept-Encoding")
        if let start {
            request.setValue("bytes=\(start)-\(end.map(String.init) ?? "")", forHTTPHeaderField: "Range")
            if let etag { request.setValue(etag, forHTTPHeaderField: "If-Range") }
        }
        let stream = DownloadStream(configuration: transferSession.configuration, policy: requestDelegate)
        do {
            let response = try await stream.start(request)
            if ![200, 206, 416].contains(response.statusCode) { try validateStatus(response.statusCode) }
            return (stream, response)
        } catch {
            stream.cancel()
            throw error
        }
    }

    func publicShareResponse(_ target: PublicShareTarget, passcode: String, download: Bool) async throws -> (DownloadStream, HTTPURLResponse) {
        guard ShareContract.validPasscode(passcode) else { throw PublicShareError.invalidPasscode }
        let configuration = transferSession.configuration.copy() as! URLSessionConfiguration
        // Even a caller-provided test/custom transport cannot leak account
        // headers, cookies or URL credentials into an anonymous share request.
        configuration.httpAdditionalHeaders = nil
        configuration.httpCookieStorage = nil
        configuration.httpShouldSetCookies = false
        configuration.urlCredentialStorage = nil
        configuration.urlCache = nil
        configuration.timeoutIntervalForRequest = download ? 90 : 15
        configuration.timeoutIntervalForResource = download ? 24 * 60 * 60 : 30
        var request = URLRequest(url: try ShareContract.url(origin: target.origin, code: target.code), cachePolicy: .reloadIgnoringLocalCacheData)
        if download { request.url?.append(path: "download") }
        request.httpShouldHandleCookies = false
        request.setValue("identity", forHTTPHeaderField: "Accept-Encoding")
        if !passcode.isEmpty { request.setValue(passcode, forHTTPHeaderField: "X-Share-Passcode") }
        let stream = DownloadStream(configuration: configuration, policy: AnonymousTLSPolicy(policy: requestDelegate))
        do {
            let response = try await stream.start(request)
            switch response.statusCode {
            case 200: return (stream, response)
            case 400, 422: throw PublicShareError.invalidLink
            case 401, 404: throw PublicShareError.notFound
            case 403: throw PublicShareError.passcodeRejected
            case 410: throw PublicShareError.expiredOrExhausted
            case 429: throw PublicShareError.rateLimited
            case 500...599: throw PublicShareError.unavailable
            default: throw PublicShareError.invalidResponse
            }
        } catch { stream.cancel(); throw error }
    }

    func auditExportResponse(_ origin: ServerOrigin, token: String, userID: Int64, selection: AuditExportSelection) async throws -> AuditExportStream {
        guard userID > 0, selection.snapshotID > 0 else { throw ServerError.invalidOperation }
        let query = selection.query.parameters + [URLQueryItem(name:"cursor_mode",value:"1"),URLQueryItem(name:"snapshot_id",value:String(selection.snapshotID)),URLQueryItem(name:"limit",value:"5000")]
        var request = URLRequest(url:origin.endpoint("api/operation-logs/export").appending(queryItems:query),cachePolicy:.reloadIgnoringLocalCacheData)
        request.setValue("Bearer \(token)",forHTTPHeaderField:"Authorization")
        request.setValue("identity",forHTTPHeaderField:"Accept-Encoding")
        let stream = DownloadStream(configuration:transferSession.configuration,policy:requestDelegate)
        do {
            let response = try await stream.start(request)
            try validateStatus(response.statusCode)
            let descriptor = try AuditExportDescriptor(response:response,selection:selection,userID:userID)
            try Task.checkCancellation()
            return AuditExportStream(stream:stream,descriptor:descriptor)
        } catch { stream.cancel(); throw error }
    }

    private func validateStatus(_ status: Int, login: Bool = false, successStatus: Int = 200) throws {
        if status == successStatus { return }
        switch status {
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
