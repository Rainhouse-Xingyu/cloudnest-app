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

// MARK: - Upload DTOs

struct UploadInitRequestDTO: Encodable {
    let parentId: Int64
    let filename: String
    let totalSize: Int64
    let chunkSize: Int
    let totalChunks: Int
    
    enum CodingKeys: String, CodingKey {
        case parentId = "parent_id"
        case filename
        case totalSize = "total_size"
        case chunkSize = "chunk_size"
        case totalChunks = "total_chunks"
    }
}

struct UploadInitResponseDTO: Decodable {
    let uploadId: String
    
    enum CodingKeys: String, CodingKey {
        case uploadId = "upload_id"
    }
}

struct UploadCompleteRequestDTO: Encodable {
    let uploadId: String
    let parentId: Int64
    
    enum CodingKeys: String, CodingKey {
        case uploadId = "upload_id"
        case parentId = "parent_id"
    }
}

// MARK: - Live Photo DTOs

struct LivePhotoLookupResponseDTO: Decodable {
    let item: LivePhotoLookupItemDTO?
}

struct LivePhotoLookupItemDTO: Decodable {
    let id: Int64
    let fileRole: String
    let imageFileId: Int64
    let videoFileId: Int64
    let imageFile: LivePhotoSimpleFileDTO
    let videoFile: LivePhotoSimpleFileDTO

    enum CodingKeys: String, CodingKey {
        case id
        case fileRole = "file_role"
        case imageFileId = "image_file_id"
        case videoFileId = "video_file_id"
        case imageFile = "image_file"
        case videoFile = "video_file"
    }
}

struct LivePhotoSimpleFileDTO: Decodable {
    let id: Int64
    let filename: String
}

// MARK: - Preview Models

struct FilePreviewDocument: Identifiable {
    let id = UUID()
    let url: URL
    let displayName: String
}

struct LivePhotoPlayerItem: Identifiable {
    let id = UUID()
    let url: URL
    let displayName: String
}

// MARK: - Directory Tree Models

/// 文件目录树节点（仅目录）
struct FileTreeNode: Identifiable {
    let id: Int64
    let name: String
    let parentId: Int64
    var children: [FileTreeNode]
}

/// 当前路径节点（用于面包屑展示）
struct FilePathNode: Identifiable {
    let id: Int64
    let name: String
}


