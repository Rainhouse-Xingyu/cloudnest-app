import Foundation

/// Files 模块网络返回最外层包装
struct FileResponseDTO: Decodable {
    let items: [FileItemDTO]
}

/// Files 真实文件项 JSON 映射（对接 Go 后端）
struct FileItemDTO: Decodable, Identifiable {
    let id: Int64
    let userId: Int64
    let filename: String
    let parentId: Int64
    let isFolder: Int8
    let fileType: String
    let size: Int64
    let storageId: Int64?
    let workspaceId: Int64?
    let currentVersion: Int
    let isDeleted: Int8
    let createTime: String
    let updateTime: String
    
    enum CodingKeys: String, CodingKey {
        case id
        case userId = "user_id"
        case filename = "filename"
        case parentId = "parent_id"
        case isFolder = "is_folder"
        case fileType = "file_type"
        case size = "size"
        case storageId = "storage_id"
        case workspaceId = "workspace_id"
        case currentVersion = "current_version"
        case isDeleted = "is_deleted"
        case createTime = "create_time"
        case updateTime = "update_time"
    }
}

