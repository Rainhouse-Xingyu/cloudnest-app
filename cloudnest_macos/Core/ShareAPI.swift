import Foundation

struct ShareLink: Identifiable, Equatable, Sendable {
    let id: Int64
    let fileID: Int64?
    let fileName: String?
    let ownerID: Int64
    let code: String
    let expiresAt: Date?
    let createdAt: Date
    let requiresPasscode: Bool
    let maxDownloads: Int64
    let downloadCount: Int64
    var exhausted: Bool { maxDownloads > 0 && downloadCount >= maxDownloads }
    var expired: Bool { expiresAt.map { $0 <= Date() } ?? false }
}

// Capabilities and passcodes are kept in view memory, never a transfer journal.
struct CreatedShare: Sendable {
    let fileID: Int64
    let code: String
    let expiresAt: Date?
    let requiresPasscode: Bool
    let maxDownloads: Int64
}

enum ShareContract {
    static func validCode(_ value: String) -> Bool {
        (1...20).contains(value.utf8.count) && value.utf8.allSatisfy {
            (48...57).contains($0) || (65...90).contains($0) || (97...122).contains($0)
        }
    }
    static func validPasscode(_ value: String) -> Bool {
        value.isEmpty || (6...72).contains(value.utf8.count) && value.utf8.allSatisfy { (33...126).contains($0) }
    }
    static func date(_ value: String) throws -> Date {
        guard value.utf8.count <= 128 else { throw ServerError.invalidResponse }
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        guard let date = formatter.date(from: value) ?? ISO8601DateFormatter().date(from: value) else { throw ServerError.invalidResponse }
        return date
    }
    static func url(origin: ServerOrigin, code: String) throws -> URL {
        guard validCode(code) else { throw ServerError.invalidOperation }
        return origin.endpoint("api/public/shares/\(code)")
    }
}

extension ServerAPI {
    func shares(_ origin: ServerOrigin, token: String, expectedOwnerID: Int64) async throws -> [ShareLink] {
        struct Row: Decodable {
            let id: Int64; let file_id: Int64?; let file_name: String?; let user_id: Int64?; let share_code: String
            let expire_time: String?; let create_time: String
            let requires_passcode: Bool; let max_downloads: Int64; let download_count: Int64
        }
        struct Response: Decodable { let items: [Row]? }
        guard expectedOwnerID > 0 else { throw ServerError.invalidOperation }
        let response: Response = try await request(origin, "api/shares", token: token, maximumBytes: 4 * 1024 * 1024)
        let rows = response.items ?? []
        guard rows.count <= 5000, Set(rows.map(\.id)).count == rows.count,
              Set(rows.map(\.share_code)).count == rows.count else { throw ServerError.invalidResponse }
        return try rows.map { row in
            guard row.id > 0, row.user_id == expectedOwnerID, row.file_id.map({ $0 > 0 }) ?? true,
                  ShareContract.validCode(row.share_code), (0...1_000_000).contains(row.max_downloads),
                  row.download_count >= 0, row.file_name.map({ !$0.isEmpty && $0.utf8.count <= 4096 }) ?? true else { throw ServerError.invalidResponse }
            return ShareLink(id: row.id, fileID: row.file_id, fileName: row.file_name, ownerID: expectedOwnerID, code: row.share_code,
                expiresAt: try row.expire_time.map(ShareContract.date), createdAt: try ShareContract.date(row.create_time),
                requiresPasscode: row.requires_passcode, maxDownloads: row.max_downloads, downloadCount: row.download_count)
        }
    }

    func createShare(_ origin: ServerOrigin, token: String, fileID: Int64, expireHours: Int, passcode: String, maxDownloads: Int64) async throws -> CreatedShare {
        struct Input: Encodable { let expire_hours: Int; let passcode: String; let max_downloads: Int64 }
        struct Response: Decodable {
            let share_code: String; let expire_time: String?; let requires_passcode: Bool; let max_downloads: Int64; let access_url: String
        }
        guard fileID > 0, (0...8760).contains(expireHours), ShareContract.validPasscode(passcode),
              (0...1_000_000).contains(maxDownloads) else { throw ServerError.invalidOperation }
        let result: Response = try await request(origin, "api/files/\(fileID)/share", token: token,
            body: JSONEncoder().encode(Input(expire_hours: expireHours, passcode: passcode, max_downloads: maxDownloads)), method: "POST")
        guard ShareContract.validCode(result.share_code), result.share_code.utf8.count == 12,
              result.access_url == "/api/public/shares/\(result.share_code)",
              result.requires_passcode == !passcode.isEmpty, result.max_downloads == maxDownloads,
              (expireHours == 0) == (result.expire_time == nil) else { throw ServerError.invalidResponse }
        return CreatedShare(fileID: fileID, code: result.share_code, expiresAt: try result.expire_time.map(ShareContract.date),
            requiresPasscode: result.requires_passcode, maxDownloads: result.max_downloads)
    }

    func revokeShare(_ origin: ServerOrigin, token: String, code: String) async throws {
        struct Response: Decodable { let message: String }
        guard ShareContract.validCode(code) else { throw ServerError.invalidOperation }
        let result: Response = try await request(origin, "api/shares/\(code)", token: token, method: "DELETE")
        guard result.message == "share revoked" else { throw ServerError.invalidResponse }
    }
}
