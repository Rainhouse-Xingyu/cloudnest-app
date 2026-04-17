import Foundation

/// Files 模块服务协议，对接真实 API
protocol FilesServiceProtocol {
    func loadFiles(parentId: Int64) async throws -> [FileItemDTO]
    func initUpload(req: UploadInitRequestDTO) async throws -> UploadInitResponseDTO
    func uploadChunk(uploadId: String, chunkIndex: Int, chunkMd5: String, chunkData: Data) async throws
    func completeUpload(req: UploadCompleteRequestDTO) async throws
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

