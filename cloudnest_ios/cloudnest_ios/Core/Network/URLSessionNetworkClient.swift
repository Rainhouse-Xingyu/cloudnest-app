import Foundation

/// 基于 URLSession 的网络客户端默认实现
final class URLSessionNetworkClient: NetworkClient {
    private let baseURL: String
    private let session: URLSession

    init(baseURL: String, timeout: TimeInterval = 15, session: URLSession = .shared) {
        self.baseURL = baseURL
        self.session = session
        self.timeout = timeout
    }

    private let timeout: TimeInterval

    func request<T: Decodable>(_ endpoint: APIEndpoint, decode type: T.Type) async throws -> T {
        let data = try await request(endpoint)
        do {
            return try JSONDecoder().decode(T.self, from: data)
        } catch {
            throw NetworkError.decoding(error)
        }
    }

    func request(_ endpoint: APIEndpoint) async throws -> Data {
        var request = try buildURLRequest(from: endpoint)
        request.timeoutInterval = timeout

        let data: Data
        let response: URLResponse

        do {
            (data, response) = try await session.data(for: request)
        } catch {
            throw NetworkError.transport(error)
        }

        guard let http = response as? HTTPURLResponse else {
            throw NetworkError.invalidResponse
        }

        guard (200...299).contains(http.statusCode) else {
            throw NetworkError.httpStatus(http.statusCode, data)
        }

        return data
    }

    /// 将 endpoint 转换成 URLRequest，统一处理 query、header、body
    private func buildURLRequest(from endpoint: APIEndpoint) throws -> URLRequest {
        guard var components = URLComponents(string: baseURL + endpoint.path) else {
            throw NetworkError.invalidURL
        }

        if !endpoint.queryItems.isEmpty {
            components.queryItems = endpoint.queryItems
        }

        guard let url = components.url else {
            throw NetworkError.invalidURL
        }

        var request = URLRequest(url: url)
        request.httpMethod = endpoint.method.rawValue
        request.httpBody = endpoint.body

        endpoint.headers.forEach { key, value in
            request.setValue(value, forHTTPHeaderField: key)
        }

        return request
    }
}
