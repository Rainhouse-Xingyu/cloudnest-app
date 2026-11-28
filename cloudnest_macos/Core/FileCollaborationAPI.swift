import Foundation

enum FilePermission: String, Codable, CaseIterable, Sendable {
    case read, write
    var title: String { self == .read ? "只读" : "读写" }
}

struct FileCollaborator: Identifiable, Equatable, Sendable {
    let fileID: Int64
    let userID: Int64
    let username: String
    let permission: FilePermission
    var id: Int64 { userID }
}

extension ServerAPI {
    func fileCollaborators(_ origin: ServerOrigin, token: String, fileID: Int64) async throws -> [FileCollaborator] {
        struct Row: Decodable { let user_id: Int64; let username: String; let permission: FilePermission }
        struct Response: Decodable { let items: [Row]? }
        guard fileID > 0 else { throw ServerError.invalidOperation }
        let response: Response = try await request(origin, "api/files/\(fileID)/collaborators", token: token, maximumBytes: 2 * 1024 * 1024)
        let rows = response.items ?? []
        guard rows.count <= 5000 else { throw ServerError.invalidResponse }
        var items: [FileCollaborator] = []
        var offsets: [Int64: Int] = [:]
        for row in rows {
            guard row.user_id > 0, !row.username.isEmpty, row.username.utf8.count <= 400 else { throw ServerError.invalidResponse }
            if let index = offsets[row.user_id] {
                let old = items[index]
                guard old.username == row.username else { throw ServerError.invalidResponse }
                // Old databases can contain duplicate grants. Match the server's
                // effective read/write union, while keeping a single stable UI row.
                if row.permission == .write && old.permission != .write {
                    items[index] = FileCollaborator(fileID: fileID, userID: row.user_id, username: row.username, permission: .write)
                }
            } else {
                offsets[row.user_id] = items.count
                items.append(FileCollaborator(fileID: fileID, userID: row.user_id, username: row.username, permission: row.permission))
            }
        }
        return items
    }

    func addFileCollaborator(_ origin: ServerOrigin, token: String, fileID: Int64, username: String, permission: FilePermission) async throws {
        struct Input: Encodable { let username: String; let permission: FilePermission; let create_only = true }
        let clean = username.trimmingCharacters(in: .whitespacesAndNewlines)
        guard fileID > 0, !clean.isEmpty, clean.utf8.count <= 400,
              clean.rangeOfCharacter(from: .controlCharacters) == nil else { throw ServerError.invalidOperation }
        try await writeFileCollaborator(origin, token: token, path: "api/files/\(fileID)/collaborators",
            body: JSONEncoder().encode(Input(username: clean, permission: permission)), method: "POST", message: "file permission updated")
    }

    func changeFileCollaborator(_ origin: ServerOrigin, token: String, fileID: Int64, original: FileCollaborator, permission: FilePermission?) async throws {
        struct Input: Encodable { let user_id: Int64; let permission: FilePermission?; let expected_permission: FilePermission }
        guard fileID > 0, original.fileID == fileID, original.userID > 0 else { throw ServerError.invalidOperation }
        let path = "api/files/\(fileID)/collaborators" + (permission == nil ? "/\(original.userID)" : "")
        try await writeFileCollaborator(origin, token: token, path: path,
            body: JSONEncoder().encode(Input(user_id: original.userID, permission: permission, expected_permission: original.permission)),
            method: permission == nil ? "DELETE" : "POST", message: permission == nil ? "file permission revoked" : "file permission updated")
    }

    private func writeFileCollaborator(_ origin: ServerOrigin, token: String, path: String, body: Data, method: String, message: String) async throws {
        struct Response: Decodable { let message: String }
        let result: Response = try await request(origin, path, token: token, body: body, method: method, maximumBytes: 64 * 1024)
        guard result.message == message else { throw ServerError.invalidResponse }
    }
}
