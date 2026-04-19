import Foundation

protocol SmartServiceProtocol {
    func ocr(_ request: AIOCRRequestDTO) async throws -> AIOCRResponseDTO
    func search(keyword: String, fileType: String, location: String, landmark: String, startTime: String, endTime: String, tagOnly: Bool, page: Int, limit: Int) async throws -> AISearchResponseDTO
    func nlSearch(_ request: AINLSearchRequestDTO) async throws -> AINLSearchResponseDTO
    func summarize(_ request: AISummarizeRequestDTO) async throws -> AISummaryResponseDTO
    func askAssistant(_ request: AIAssistantAskRequestDTO) async throws -> AIAssistantAskResponseDTO
    func applySetting(_ request: AIAssistantSettingRequestDTO) async throws -> AISettingResponseDTO
}

final class SmartService: SmartServiceProtocol {
    private let networkClient: NetworkClient

    init(networkClient: NetworkClient = AppContainer.shared.networkClient) {
        self.networkClient = networkClient
    }

    private var token: String {
        get throws {
            guard let tk = SessionManager.shared.loadSession()?.token, !tk.isEmpty else {
                throw NetworkError.invalidResponse
            }
            return tk
        }
    }

    func ocr(_ request: AIOCRRequestDTO) async throws -> AIOCRResponseDTO {
        let body = try JSONEncoder().encode(request)
        let endpoint = GenericPostEndpoint(path: "/api/ai/ocr", body: body, token: try token)
        return try await networkClient.request(endpoint, decode: AIOCRResponseDTO.self)
    }

    func search(keyword: String, fileType: String, location: String, landmark: String, startTime: String, endTime: String, tagOnly: Bool, page: Int, limit: Int) async throws -> AISearchResponseDTO {
        let endpoint = AISearchEndpoint(
            keyword: keyword,
            fileType: fileType,
            location: location,
            landmark: landmark,
            startTime: startTime,
            endTime: endTime,
            tagOnly: tagOnly,
            page: page,
            limit: limit,
            token: try token
        )
        return try await networkClient.request(endpoint, decode: AISearchResponseDTO.self)
    }

    func nlSearch(_ request: AINLSearchRequestDTO) async throws -> AINLSearchResponseDTO {
        let body = try JSONEncoder().encode(request)
        let endpoint = GenericPostEndpoint(path: "/api/ai/search/nl", body: body, token: try token)
        return try await networkClient.request(endpoint, decode: AINLSearchResponseDTO.self)
    }

    func summarize(_ request: AISummarizeRequestDTO) async throws -> AISummaryResponseDTO {
        let body = try JSONEncoder().encode(request)
        let endpoint = GenericPostEndpoint(path: "/api/ai/summarize", body: body, token: try token)
        return try await networkClient.request(endpoint, decode: AISummaryResponseDTO.self)
    }

    func askAssistant(_ request: AIAssistantAskRequestDTO) async throws -> AIAssistantAskResponseDTO {
        let body = try JSONEncoder().encode(request)
        let endpoint = GenericPostEndpoint(path: "/api/ai/assistant/ask", body: body, token: try token)
        return try await networkClient.request(endpoint, decode: AIAssistantAskResponseDTO.self)
    }

    func applySetting(_ request: AIAssistantSettingRequestDTO) async throws -> AISettingResponseDTO {
        let body = try JSONEncoder().encode(request)
        let endpoint = GenericPostEndpoint(path: "/api/ai/assistant/settings", body: body, token: try token)
        return try await networkClient.request(endpoint, decode: AISettingResponseDTO.self)
    }
}

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

private struct AISearchEndpoint: APIEndpoint {
    let keyword: String
    let fileType: String
    let location: String
    let landmark: String
    let startTime: String
    let endTime: String
    let tagOnly: Bool
    let page: Int
    let limit: Int
    let token: String

    var path: String { "/api/ai/search" }
    var method: HTTPMethod { .get }
    var queryItems: [URLQueryItem] {
        [
            URLQueryItem(name: "keyword", value: keyword),
            URLQueryItem(name: "file_type", value: fileType),
            URLQueryItem(name: "location", value: location),
            URLQueryItem(name: "landmark", value: landmark),
            URLQueryItem(name: "start_time", value: startTime),
            URLQueryItem(name: "end_time", value: endTime),
            URLQueryItem(name: "tag_only", value: tagOnly ? "true" : "false"),
            URLQueryItem(name: "page", value: "\(page)"),
            URLQueryItem(name: "limit", value: "\(limit)")
        ]
    }
    var headers: [String: String] {
        [
            "Authorization": "Bearer \(token)",
            "Accept": "application/json"
        ]
    }
}
