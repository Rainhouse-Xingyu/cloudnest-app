import Foundation

enum PublicShareError: LocalizedError, Equatable {
    case invalidLink, invalidPasscode, passcodeRejected, notFound, expiredOrExhausted, rateLimited, unavailable, invalidResponse
    var errorDescription: String? {
        switch self {
        case .invalidLink: "请输入完整的 CloudNest HTTPS 分享链接，不含查询参数或片段。"
        case .invalidPasscode: "提取码须为 6–72 个可见 ASCII 字符，不含空格。"
        case .passcodeRejected: "分享需要提取码，或输入的提取码不正确。"
        case .notFound: "分享已撤销、文件不可访问，或链接不存在。"
        case .expiredOrExhausted: "分享已到期或下载次数已用尽。"
        case .rateLimited: "分享访问过于频繁，请稍后再试。"
        case .unavailable: "分享服务器暂时不可用，请检查连接。"
        case .invalidResponse: "分享服务器返回了无法验证的内容。"
        }
    }
}

struct PublicShareTarget: Equatable, Sendable {
    let origin: ServerOrigin
    let code: String

    init(_ input: String) throws {
        let clean = input.trimmingCharacters(in: .whitespacesAndNewlines)
        guard clean.utf8.count <= 8192, var parts = URLComponents(string: clean),
              parts.query == nil, parts.fragment == nil else { throw PublicShareError.invalidLink }
        let prefix = "/api/public/shares/"
        guard parts.percentEncodedPath.hasPrefix(prefix) else { throw PublicShareError.invalidLink }
        let code = String(parts.percentEncodedPath.dropFirst(prefix.count))
        guard ShareContract.validCode(code) else { throw PublicShareError.invalidLink }
        parts.path = ""
        guard let root = parts.string, let origin = try? ServerOrigin(root) else { throw PublicShareError.invalidLink }
        self.origin = origin; self.code = code
    }
}

struct PublicShareMetadata: Equatable, Sendable {
    let target: PublicShareTarget
    let fileID: Int64
    let filename: String
    let isDirectory: Bool
    let size: Int64
    let expiresAt: Date?
    let requiresPasscode: Bool
    let maxDownloads: Int64
    let downloadCount: Int64
}

// No account, JWT, durable capability store, cookies, or automatic replay.
struct PublicShareClient: Sendable {
    private let api: ServerAPI
    init(api: ServerAPI) { self.api = api }

    func metadata(_ target: PublicShareTarget, passcode: String) async throws -> PublicShareMetadata {
        struct File: Decodable { let id: Int64; let filename: String; let is_folder: Int; let size: Int64 }
        struct Response: Decodable {
            let share_code: String; let expire_time: String?; let requires_passcode: Bool
            let max_downloads: Int64; let download_count: Int64; let file: File
        }
        let (stream, _) = try await api.publicShareResponse(target, passcode: passcode, download: false)
        defer { stream.cancel() }
        return try await withTaskCancellationHandler {
            var data = Data()
            for try await chunk in stream.chunks {
                defer { chunk.acknowledge() }
                try Task.checkCancellation()
                guard chunk.data.count <= 64 * 1024 - data.count else { throw PublicShareError.invalidResponse }
                data.append(chunk.data)
            }
            try Task.checkCancellation()
            let row: Response
            do { row = try JSONDecoder().decode(Response.self, from: data) }
            catch { throw PublicShareError.invalidResponse }
            guard row.share_code == target.code, row.file.id > 0, !row.file.filename.isEmpty,
                  row.file.filename.utf8.count <= 1024, row.file.filename.rangeOfCharacter(from: .controlCharacters) == nil,
                  row.file.filename != ".", row.file.filename != "..", !row.file.filename.contains("/"),
                  [0, 1].contains(row.file.is_folder), (0...Int64(1 << 40)).contains(row.file.size),
                  (0...1_000_000).contains(row.max_downloads), row.download_count >= 0 else { throw PublicShareError.invalidResponse }
            let expiry: Date?
            do { expiry = try row.expire_time.map(ShareContract.date) }
            catch { throw PublicShareError.invalidResponse }
            return PublicShareMetadata(target: target, fileID: row.file.id, filename: row.file.filename,
                isDirectory: row.file.is_folder == 1, size: row.file.size, expiresAt: expiry,
                requiresPasscode: row.requires_passcode, maxDownloads: row.max_downloads, downloadCount: row.download_count)
        } onCancel: { stream.cancel() }
    }

    func downloadResponse(_ target: PublicShareTarget, passcode: String) async throws -> (DownloadStream, HTTPURLResponse) {
        try await api.publicShareResponse(target, passcode: passcode, download: true)
    }
}
