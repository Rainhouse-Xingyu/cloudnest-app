import Foundation

enum WorkspaceRole: String, Codable, CaseIterable, Sendable {
    case owner, admin, editor, viewer, member
    var canWrite: Bool { [.owner, .admin, .editor].contains(self) }
    var canManageMembers: Bool { self == .owner || self == .admin }
    var title: String {
        switch self {
        case .owner: "所有者"
        case .admin: "空间管理员"
        case .editor: "编辑者"
        case .viewer: "查看者"
        case .member: "旧版成员（只读）"
        }
    }
}

struct Workspace: Decodable, Identifiable, Equatable, Sendable {
    let id: Int64
    let name: String
    let ownerID: Int64
    let rootFileID: Int64?
    let role: WorkspaceRole
    let createdAt: String
    enum CodingKeys: String, CodingKey {
        case id, name, role
        case ownerID = "owner_id", rootFileID = "root_file_id", createdAt = "create_time"
    }
    func validate() throws {
        guard id > 0, ownerID > 0, !name.isEmpty, name.utf8.count <= 400,
              rootFileID.map({ $0 > 0 }) ?? true, createdAt.utf8.count <= 128 else { throw ServerError.invalidResponse }
    }
    func canManage(_ member: WorkspaceMember) -> Bool {
        guard member.userID != ownerID, member.role != .owner else { return false }
        return role == .owner || role == .admin && member.role != .admin
    }
}

struct WorkspaceMember: Identifiable, Sendable {
    let workspaceID: Int64
    let userID: Int64
    let username: String
    let role: WorkspaceRole
    var id: Int64 { userID }
}

struct FileBrowserRoot: Hashable, Sendable {
    let id: Int64
    let title: String
    let workspaceID: Int64?
    let canWrite: Bool
    static let personal = FileBrowserRoot(id: 0, title: "我的文件", workspaceID: nil, canWrite: true)
    init?(workspace: Workspace) {
        guard let root = workspace.rootFileID, root > 0 else { return nil }
        id = root; title = workspace.name; workspaceID = workspace.id; canWrite = workspace.role.canWrite
    }
    private init(id: Int64, title: String, workspaceID: Int64?, canWrite: Bool) {
        self.id = id; self.title = title; self.workspaceID = workspaceID; self.canWrite = canWrite
    }
}

extension ServerAPI {
    func workspaces(_ origin: ServerOrigin, token: String) async throws -> [Workspace] {
        struct Response: Decodable { let items: [Workspace]? }
        let result: Response = try await request(origin, "api/workspaces", token: token, maximumBytes: 4 * 1024 * 1024)
        let items = result.items ?? []
        guard items.count <= 5000, Set(items.map(\.id)).count == items.count else { throw ServerError.invalidResponse }
        for row in items { try row.validate() }
        return items
    }

    func createWorkspace(_ origin: ServerOrigin, token: String, name: String, expectedOwnerID: Int64) async throws -> Workspace {
        struct Input: Encodable { let name: String }
        struct Created: Decodable { let id: Int64; let name: String; let owner_id: Int64; let root_file_id: Int64?; let create_time: String }
        struct Response: Decodable { let item: Created }
        let clean = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !clean.isEmpty, clean.unicodeScalars.count <= 100, clean.rangeOfCharacter(from: .controlCharacters) == nil, expectedOwnerID > 0 else { throw ServerError.invalidOperation }
        let result: Response = try await request(origin, "api/workspaces", token: token,
            body: try JSONEncoder().encode(Input(name: clean)), method: "POST", maximumBytes: 64 * 1024)
        let item = Workspace(id: result.item.id, name: result.item.name, ownerID: result.item.owner_id,
            rootFileID: result.item.root_file_id, role: .owner, createdAt: result.item.create_time)
        try item.validate()
        guard item.ownerID == expectedOwnerID, item.name == clean, item.rootFileID != nil else { throw ServerError.invalidResponse }
        return item
    }

    func workspaceMembers(_ origin: ServerOrigin, token: String, workspaceID: Int64) async throws -> [WorkspaceMember] {
        struct Row: Decodable { let user_id: Int64; let username: String; let role: WorkspaceRole }
        struct Response: Decodable { let items: [Row]? }
        guard workspaceID > 0 else { throw ServerError.invalidOperation }
        let result: Response = try await request(origin, "api/workspaces/\(workspaceID)/members", token: token, maximumBytes: 2 * 1024 * 1024)
        let items = result.items ?? []
        guard items.count <= 5000, Set(items.map(\.user_id)).count == items.count,
              items.allSatisfy({ $0.user_id > 0 && !$0.username.isEmpty && $0.username.utf8.count <= 400 }) else { throw ServerError.invalidResponse }
        return items.map { WorkspaceMember(workspaceID: workspaceID, userID: $0.user_id, username: $0.username, role: $0.role) }
    }

    func addWorkspaceMember(_ origin: ServerOrigin, token: String, workspaceID: Int64, username: String, role: WorkspaceRole) async throws {
        struct Input: Encodable { let username: String; let role: String; let create_only = true }
        struct Response: Decodable { let message: String }
        let clean = username.trimmingCharacters(in: .whitespacesAndNewlines)
        guard workspaceID > 0, role != .owner, !clean.isEmpty, clean.utf8.count <= 400,
              clean.rangeOfCharacter(from: .controlCharacters) == nil else { throw ServerError.invalidOperation }
        let result: Response = try await request(origin, "api/workspaces/\(workspaceID)/members", token: token,
            body: try JSONEncoder().encode(Input(username: clean, role: role.rawValue)), method: "POST")
        guard result.message == "member updated" else { throw ServerError.invalidResponse }
    }

    func changeWorkspaceMember(_ origin: ServerOrigin, token: String, workspaceID: Int64, original: WorkspaceMember, role: WorkspaceRole?) async throws {
        struct Input: Encodable { let role: String?; let expected_role: String }
        struct Response: Decodable { let message: String }
        guard workspaceID > 0, original.workspaceID == workspaceID, original.userID > 0, original.role != .owner, role != .owner else { throw ServerError.invalidOperation }
        let result: Response = try await request(origin, "api/workspaces/\(workspaceID)/members/\(original.userID)", token: token,
            body: try JSONEncoder().encode(Input(role: role?.rawValue, expected_role: original.role.rawValue)), method: role == nil ? "DELETE" : "PUT")
        guard result.message == (role == nil ? "member removed" : "member role updated") else { throw ServerError.invalidResponse }
    }
}
