import Foundation

struct FileItem: Decodable, Identifiable, Equatable, Sendable {
    let id: Int64
    let filename: String
    let parentID: Int64
    let isFolder: Int
    let size: Int64
    let currentVersion: Int
    let updateTime: String
    let fileType: String

    var isDirectory: Bool { isFolder == 1 }
    var modified: Date? {
        let fractional = ISO8601DateFormatter()
        fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return fractional.date(from: updateTime) ?? ISO8601DateFormatter().date(from: updateTime)
    }

    enum CodingKeys: String, CodingKey {
        case id, filename, size
        case parentID = "parent_id"
        case isFolder = "is_folder"
        case currentVersion = "current_version"
        case updateTime = "update_time"
        case fileType = "file_type"
    }
}

struct DeleteFilesResponse: Decodable, Sendable {
    let deletedIDs: [Int64]?
    let failed: [String: String]

    enum CodingKeys: String, CodingKey {
        case deletedIDs = "deleted_ids"
        case failed
    }
}

extension ServerAPI {
    private struct FileList: Decodable { let items: [FileItem]? }
    private struct FileResponse: Decodable { let item: FileItem }
    private struct FolderInput: Encodable {
        let name: String
        let parent_id: Int64
    }
    private struct RenameInput: Encodable { let name: String }
    private struct SelectionInput: Encodable { let file_ids: [Int64] }

    func files(_ origin: ServerOrigin, token: String, parentID: Int64, recycleBin: Bool) async throws -> [FileItem] {
        let path = recycleBin ? "api/files/recycle-bin" : "api/files"
        let query = recycleBin ? [] : [URLQueryItem(name: "parent_id", value: String(parentID))]
        let result: FileList = try await request(origin, path, token: token, query: query, maximumBytes: 8 * 1024 * 1024)
        let items = result.items ?? []
        guard items.allSatisfy({ $0.id > 0 && $0.parentID >= 0 && $0.size >= 0 }),
              Set(items.map(\.id)).count == items.count else { throw ServerError.invalidResponse }
        return items
    }

    func createFolder(_ origin: ServerOrigin, token: String, name: String, parentID: Int64) async throws -> FileItem {
        let body = try JSONEncoder().encode(FolderInput(name: name, parent_id: parentID))
        let response: FileResponse = try await request(origin, "api/files/folders", token: token,
                                                      body: body, method: "POST")
        return response.item
    }

    func renameFile(_ origin: ServerOrigin, token: String, id: Int64, name: String) async throws {
        let body = try JSONEncoder().encode(RenameInput(name: name))
        let _: FileResponse = try await request(origin, "api/files/\(id)/rename", token: token,
                                                body: body, method: "PUT")
    }

    func trashFiles(_ origin: ServerOrigin, token: String, ids: [Int64]) async throws -> DeleteFilesResponse {
        let body = try JSONEncoder().encode(SelectionInput(file_ids: ids))
        return try await request(origin, "api/files/batch-delete", token: token,
                                                     body: body, method: "POST")
    }

    func restoreFile(_ origin: ServerOrigin, token: String, id: Int64) async throws {
        let _: FileResponse = try await request(origin, "api/files/\(id)/restore", token: token, method: "POST")
    }
}
