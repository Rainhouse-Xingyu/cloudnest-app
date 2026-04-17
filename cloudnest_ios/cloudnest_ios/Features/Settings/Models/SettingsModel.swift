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
