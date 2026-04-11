import Foundation

/// Files 模块服务协议，对接真实 API
protocol FilesServiceProtocol {
    func loadFiles(parentId: Int64) async throws -> [FileItemDTO]
}

/// Files 模块服务默认实现
final class FilesService: FilesServiceProtocol {
    private let networkClient: NetworkClient

    init(networkClient: NetworkClient = AppContainer.shared.networkClient) {
        self.networkClient = networkClient
    }

    func loadFiles(parentId: Int64 = 0) async throws -> [FileItemDTO] {
        guard let token = SessionManager.shared.currentSession?.token else {
            throw NetworkError.invalidURL // 或自定义未登录错误
        }
        let endpoint = FilesListEndpoint(parentId: parentId, token: token)
        let response = try await networkClient.request(endpoint, decode: FileResponseDTO.self)
        return response.items
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

