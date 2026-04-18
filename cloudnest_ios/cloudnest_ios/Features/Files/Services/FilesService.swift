import Foundation

/// Files 模块服务协议，对接真实 API
protocol FilesServiceProtocol {
    func loadFiles(parentId: Int64) async throws -> [FileItemDTO]
    func searchFiles(parentId: Int64, name: String, fileType: String, startTime: String?, endTime: String?, page: Int, limit: Int) async throws -> [FileItemDTO]
    func loadDirectoryTree() async throws -> [FileTreeNode]
    func createFolder(parentId: Int64, name: String) async throws
    func batchDelete(fileIds: [Int64]) async throws -> [Int64]
    func move(fileIds: [Int64], targetParentId: Int64) async throws
    func copy(fileIds: [Int64], targetParentId: Int64) async throws
    func createShare(fileId: Int64, expireHours: Int?) async throws -> CreateShareResponseDTO
    func listShares() async throws -> [ShareItemDTO]
    func revokeShare(code: String) async throws
    func listRecycleBin() async throws -> [FileItemDTO]
    func restoreFromRecycleBin(fileId: Int64) async throws
    func permanentDelete(fileId: Int64) async throws
    func initUpload(req: UploadInitRequestDTO) async throws -> UploadInitResponseDTO
    func uploadChunk(uploadId: String, chunkIndex: Int, chunkMd5: String, chunkData: Data) async throws
    func completeUpload(req: UploadCompleteRequestDTO) async throws
    func downloadFile(fileId: Int64, filename: String, isFolder: Bool) async throws -> URL
    func getLivePhoto(byFileId fileId: Int64) async throws -> LivePhotoLookupItemDTO?
    func autoBindLivePhotos(parentId: Int64?, fileIds: [Int64]) async throws
}

/// Files 模块服务默认实现
final class FilesService: FilesServiceProtocol {
    private let networkClient: NetworkClient

    init(networkClient: NetworkClient = AppContainer.shared.networkClient) {
        self.networkClient = networkClient
    }

    private var token: String {
        get throws {
            guard let tk = SessionManager.shared.loadSession()?.token else {
                throw NetworkError.invalidURL
            }
            return tk
        }
    }

    func loadFiles(parentId: Int64 = 0) async throws -> [FileItemDTO] {
        let endpoint = FilesListEndpoint(parentId: parentId, token: try token)
        let response = try await networkClient.request(endpoint, decode: FileResponseDTO.self)
        return response.items
    }

    func searchFiles(parentId: Int64, name: String, fileType: String, startTime: String?, endTime: String?, page: Int, limit: Int) async throws -> [FileItemDTO] {
        let endpoint = FilesSearchEndpoint(
            parentId: parentId,
            name: name,
            fileType: fileType,
            startTime: startTime,
            endTime: endTime,
            page: page,
            limit: limit,
            token: try token
        )
        let response = try await networkClient.request(endpoint, decode: SearchFilesResponseDTO.self)
        return response.items
    }

    func loadDirectoryTree() async throws -> [FileTreeNode] {
        try await fetchTree(parentId: 0, depth: 0, maxDepth: 8)
    }

    func createFolder(parentId: Int64, name: String) async throws {
        let body = try JSONEncoder().encode(CreateFolderRequestDTO(parentId: parentId, name: name))
        let endpoint = GenericPostEndpoint(path: "/api/files/folders", body: body, token: try token)
        let _ = try await networkClient.request(endpoint)
    }

    func batchDelete(fileIds: [Int64]) async throws -> [Int64] {
        let body = try JSONEncoder().encode(BatchFileOperationRequestDTO(fileIds: fileIds))
        let endpoint = GenericPostEndpoint(path: "/api/files/batch-delete", body: body, token: try token)
        let response = try await networkClient.request(endpoint, decode: BatchDeleteResponseDTO.self)
        return response.deletedIds
    }

    func move(fileIds: [Int64], targetParentId: Int64) async throws {
        let body = try JSONEncoder().encode(MoveOrCopyRequestDTO(fileIds: fileIds, targetParentId: targetParentId))
        let endpoint = GenericPostEndpoint(path: "/api/files/move", body: body, token: try token)
        let _ = try await networkClient.request(endpoint)
    }

    func copy(fileIds: [Int64], targetParentId: Int64) async throws {
        let body = try JSONEncoder().encode(MoveOrCopyRequestDTO(fileIds: fileIds, targetParentId: targetParentId))
        let endpoint = GenericPostEndpoint(path: "/api/files/copy", body: body, token: try token)
        let _ = try await networkClient.request(endpoint)
    }

    func createShare(fileId: Int64, expireHours: Int?) async throws -> CreateShareResponseDTO {
        let body: Data?
        if let expireHours {
            body = try JSONSerialization.data(withJSONObject: ["expire_hours": expireHours])
        } else {
            body = nil
        }
        let endpoint = GenericPostEndpoint(path: "/api/files/\(fileId)/share", body: body, token: try token)
        return try await networkClient.request(endpoint, decode: CreateShareResponseDTO.self)
    }

    func listShares() async throws -> [ShareItemDTO] {
        let endpoint = GenericGetEndpoint(path: "/api/shares", token: try token)
        let response = try await networkClient.request(endpoint, decode: ShareListResponseDTO.self)
        return response.items
    }

    func revokeShare(code: String) async throws {
        let endpoint = GenericDeleteEndpoint(path: "/api/shares/\(code)", token: try token)
        let _ = try await networkClient.request(endpoint)
    }

    func listRecycleBin() async throws -> [FileItemDTO] {
        let endpoint = GenericGetEndpoint(path: "/api/files/recycle-bin", token: try token)
        let response = try await networkClient.request(endpoint, decode: FileResponseDTO.self)
        return response.items
    }

    func restoreFromRecycleBin(fileId: Int64) async throws {
        let endpoint = GenericPostEndpoint(path: "/api/files/\(fileId)/restore", body: nil, token: try token)
        let _ = try await networkClient.request(endpoint)
    }

    func permanentDelete(fileId: Int64) async throws {
        let endpoint = GenericDeleteEndpoint(path: "/api/files/\(fileId)/permanent", token: try token)
        let _ = try await networkClient.request(endpoint)
    }

    func initUpload(req: UploadInitRequestDTO) async throws -> UploadInitResponseDTO {
        let body = try JSONEncoder().encode(req)
        let endpoint = GenericPostEndpoint(path: "/api/files/upload/init", body: body, token: try token)
        return try await networkClient.request(endpoint, decode: UploadInitResponseDTO.self)
    }

    func uploadChunk(uploadId: String, chunkIndex: Int, chunkMd5: String, chunkData: Data) async throws {
        let boundary = "Boundary-\(UUID().uuidString)"
        var body = Data()
        let parameters = [
            "upload_id": uploadId,
            "chunk_index": "\(chunkIndex)",
            "chunk_md5": chunkMd5
        ]
        
        for (key, value) in parameters {
            body.append("--\(boundary)\r\n".data(using: .utf8)!)
            body.append("Content-Disposition: form-data; name=\"\(key)\"\r\n\r\n".data(using: .utf8)!)
            body.append("\(value)\r\n".data(using: .utf8)!)
        }
        
        // chunk data
        body.append("--\(boundary)\r\n".data(using: .utf8)!)
        body.append("Content-Disposition: form-data; name=\"chunk\"; filename=\"chunk\"\r\n".data(using: .utf8)!)
        body.append("Content-Type: application/octet-stream\r\n\r\n".data(using: .utf8)!)
        body.append(chunkData)
        body.append("\r\n--\(boundary)--\r\n".data(using: .utf8)!)
        
        let endpoint = MultipartParamsEndpoint(
            path: "/api/files/upload/chunk",
            token: try token,
            boundary: boundary,
            body: body
        )
        
        let _ = try await networkClient.request(endpoint)
    }

    func completeUpload(req: UploadCompleteRequestDTO) async throws {
        let body = try JSONEncoder().encode(req)
        let endpoint = GenericPostEndpoint(path: "/api/files/upload/complete", body: body, token: try token)
        let _ = try await networkClient.request(endpoint)
    }

    func downloadFile(fileId: Int64, filename: String, isFolder: Bool) async throws -> URL {
        let endpoint = FilesDownloadEndpoint(fileId: fileId, token: try token, isFolder: isFolder)
        let data = try await networkClient.request(endpoint)

        let directory = try createDownloadDirectoryIfNeeded()
        let normalizedName = normalizedDownloadName(filename: filename, isFolder: isFolder)
        let outputURL = uniqueURL(in: directory, filename: normalizedName)
        try data.write(to: outputURL)
        return outputURL
    }

    func getLivePhoto(byFileId fileId: Int64) async throws -> LivePhotoLookupItemDTO? {
        let endpoint = LivePhotoLookupEndpoint(fileId: fileId, token: try token)
        let response = try await networkClient.request(endpoint, decode: LivePhotoLookupResponseDTO.self)
        return response.item
    }

    func autoBindLivePhotos(parentId: Int64?, fileIds: [Int64]) async throws {
        let body = try JSONEncoder().encode(
            LivePhotoAutoBindRequestDTO(parentId: parentId, fileIds: fileIds)
        )
        let endpoint = GenericPostEndpoint(path: "/api/live-photos/auto-bind", body: body, token: try token)
        let _ = try await networkClient.request(endpoint)
    }

    private func fetchTree(parentId: Int64, depth: Int, maxDepth: Int) async throws -> [FileTreeNode] {
        if depth > maxDepth {
            return []
        }

        let folders = try await loadFiles(parentId: parentId)
            .filter { $0.isFolder == 1 }
            .sorted { $0.filename.localizedCompare($1.filename) == .orderedAscending }

        var nodes: [FileTreeNode] = []
        nodes.reserveCapacity(folders.count)

        for folder in folders {
            let children = try await fetchTree(parentId: folder.id, depth: depth + 1, maxDepth: maxDepth)
            nodes.append(
                FileTreeNode(
                    id: folder.id,
                    name: folder.filename,
                    parentId: folder.parentId,
                    children: children
                )
            )
        }

        return nodes
    }

    private func createDownloadDirectoryIfNeeded() throws -> URL {
        let base = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask).first
            ?? FileManager.default.temporaryDirectory
        let folder = base.appendingPathComponent("Downloads", isDirectory: true)
        if !FileManager.default.fileExists(atPath: folder.path) {
            try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        }
        return folder
    }

    private func normalizedDownloadName(filename: String, isFolder: Bool) -> String {
        let trimmed = filename.trimmingCharacters(in: .whitespacesAndNewlines)
        if isFolder {
            if trimmed.lowercased().hasSuffix(".zip") {
                return trimmed.isEmpty ? "folder.zip" : trimmed
            }
            return (trimmed.isEmpty ? "folder" : trimmed) + ".zip"
        }
        return trimmed.isEmpty ? "file" : trimmed
    }

    private func uniqueURL(in directory: URL, filename: String) -> URL {
        let ext = (filename as NSString).pathExtension
        let baseName = (filename as NSString).deletingPathExtension
        var candidate = directory.appendingPathComponent(filename)
        var index = 1

        while FileManager.default.fileExists(atPath: candidate.path) {
            let newName: String
            if ext.isEmpty {
                newName = "\(baseName)(\(index))"
            } else {
                newName = "\(baseName)(\(index)).\(ext)"
            }
            candidate = directory.appendingPathComponent(newName)
            index += 1
        }

        return candidate
    }
}

/// 通用 POST Endpoint
private struct GenericPostEndpoint: APIEndpoint {
    let path: String
    let body: Data?
    let token: String

    var method: HTTPMethod { .post }
    var headers: [String: String] {
        [
            "Authorization": "Bearer \(token)",
            "Content-Type": "application/json",
            "Accept": "application/json"
        ]
    }
}

private struct GenericGetEndpoint: APIEndpoint {
    let path: String
    let token: String

    var method: HTTPMethod { .get }
    var headers: [String: String] {
        [
            "Authorization": "Bearer \(token)",
            "Accept": "application/json"
        ]
    }
}

private struct GenericDeleteEndpoint: APIEndpoint {
    let path: String
    let token: String

    var method: HTTPMethod { .delete }
    var headers: [String: String] {
        [
            "Authorization": "Bearer \(token)",
            "Accept": "application/json"
        ]
    }
}

/// Multipart Endpoint
private struct MultipartParamsEndpoint: APIEndpoint {
    let path: String
    let token: String
    let boundary: String
    let body: Data?

    var method: HTTPMethod { .post }
    var headers: [String: String] {
        [
            "Authorization": "Bearer \(token)",
            "Content-Type": "multipart/form-data; boundary=\(boundary)"
        ]
    }
}

/// 核心层：列表 API EndPoint
private struct FilesListEndpoint: APIEndpoint {
    let parentId: Int64
    let token: String

    var path: String { "/api/files" }
    var method: HTTPMethod { .get }
    var queryItems: [URLQueryItem] {
        [ URLQueryItem(name: "parent_id", value: "\(parentId)") ]
    }
    var headers: [String: String] {
        [
            "Authorization": "Bearer \(token)",
            "Accept": "application/json"
        ]
    }
}

private struct FilesSearchEndpoint: APIEndpoint {
    let parentId: Int64
    let name: String
    let fileType: String
    let startTime: String?
    let endTime: String?
    let page: Int
    let limit: Int
    let token: String

    var path: String { "/api/files/search" }
    var method: HTTPMethod { .get }
    var queryItems: [URLQueryItem] {
        var items: [URLQueryItem] = [
            URLQueryItem(name: "parent_id", value: "\(parentId)"),
            URLQueryItem(name: "name", value: name),
            URLQueryItem(name: "file_type", value: fileType),
            URLQueryItem(name: "page", value: "\(page)"),
            URLQueryItem(name: "limit", value: "\(limit)")
        ]
        if let startTime, !startTime.isEmpty {
            items.append(URLQueryItem(name: "start_time", value: startTime))
        }
        if let endTime, !endTime.isEmpty {
            items.append(URLQueryItem(name: "end_time", value: endTime))
        }
        return items
    }
    var headers: [String: String] {
        [
            "Authorization": "Bearer \(token)",
            "Accept": "application/json"
        ]
    }
}

/// 下载接口 EndPoint（文件 / 目录 zip）
private struct FilesDownloadEndpoint: APIEndpoint {
    let fileId: Int64
    let token: String
    let isFolder: Bool

    var path: String {
        if isFolder {
            return "/api/files/\(fileId)/download-zip"
        }
        return "/api/files/\(fileId)/download"
    }

    var method: HTTPMethod { .get }

    var headers: [String : String] {
        [
            "Authorization": "Bearer \(token)",
            "Accept": "application/octet-stream"
        ]
    }
}

private struct LivePhotoLookupEndpoint: APIEndpoint {
    let fileId: Int64
    let token: String

    var path: String { "/api/files/\(fileId)/live-photo" }
    var method: HTTPMethod { .get }
    var headers: [String: String] {
        [
            "Authorization": "Bearer \(token)",
            "Accept": "application/json"
        ]
    }
}

