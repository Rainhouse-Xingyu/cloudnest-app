import Foundation

/// 角色选项：与后端 role_name 对齐。
enum UserRoleOption: String, CaseIterable, Identifiable {
    case user
    case admin

    var id: String { rawValue }

    var title: String {
        switch self {
        case .user:
            return "普通用户"
        case .admin:
            return "管理员"
        }
    }
}

/// HTTP 方法选项，"*" 表示匹配所有方法。
enum PermissionMethodOption: String, CaseIterable, Identifiable {
    case all = "*"
    case get = "GET"
    case post = "POST"
    case put = "PUT"
    case patch = "PATCH"
    case delete = "DELETE"

    var id: String { rawValue }
}

/// 页面内编辑用的路径权限项。
struct UserPermissionItem: Identifiable, Equatable {
    let id: UUID
    let method: PermissionMethodOption
    let path: String

    init(id: UUID = UUID(), method: PermissionMethodOption, path: String) {
        self.id = id
        self.method = method
        self.path = path
    }
}

/// 创建用户请求体。
struct CreateManagedUserRequestDTO: Encodable {
    let username: String
    let password: String
    let email: String
    let roleName: String
    let permissions: [CreateManagedUserPermissionDTO]

    enum CodingKeys: String, CodingKey {
        case username
        case password
        case email
        case roleName = "role_name"
        case permissions
    }
}

struct CreateManagedUserPermissionDTO: Encodable {
    let method: String
    let path: String
}

/// 创建用户成功响应。
struct CreateManagedUserResponseDTO: Decodable {
    let id: Int64
    let username: String
}

// MARK: - Collaboration DTOs

/// 工作空间角色，与后端 owner/admin/member 对齐。
enum WorkspaceRoleOption: String, CaseIterable, Identifiable {
    case admin
    case member

    var id: String { rawValue }

    var title: String {
        switch self {
        case .admin:
            return "管理员"
        case .member:
            return "成员"
        }
    }
}

/// 文件协作者权限，与后端 read/write 对齐。
enum FileCollaboratorPermissionOption: String, CaseIterable, Identifiable {
    case read
    case write

    var id: String { rawValue }

    var title: String {
        switch self {
        case .read:
            return "只读"
        case .write:
            return "可写"
        }
    }
}

struct WorkspaceItemDTO: Decodable, Identifiable, Equatable {
    let id: Int64
    let name: String
    let ownerId: Int64
    let role: String

    enum CodingKeys: String, CodingKey {
        case id
        case name
        case ownerId = "owner_id"
        case role
    }
}

struct WorkspaceMemberDTO: Decodable, Identifiable, Equatable {
    var id: Int64 { userId }
    let userId: Int64
    let username: String
    let role: String

    enum CodingKeys: String, CodingKey {
        case userId = "user_id"
        case username
        case role
    }
}

struct FileCollaboratorDTO: Decodable, Identifiable, Equatable {
    var id: Int64 { userId }
    let userId: Int64
    let username: String
    let permission: String

    enum CodingKeys: String, CodingKey {
        case userId = "user_id"
        case username
        case permission
    }
}

struct CreateWorkspaceRequestDTO: Encodable {
    let name: String
}

struct UpsertWorkspaceMemberRequestDTO: Encodable {
    let username: String
    let role: String
}

struct GrantFileCollaboratorRequestDTO: Encodable {
    let username: String
    let permission: String
}

struct WorkspaceListResponseDTO: Decodable {
    let items: [WorkspaceItemDTO]
}

struct WorkspaceMemberListResponseDTO: Decodable {
    let items: [WorkspaceMemberDTO]
}

struct FileCollaboratorListResponseDTO: Decodable {
    let items: [FileCollaboratorDTO]
}
