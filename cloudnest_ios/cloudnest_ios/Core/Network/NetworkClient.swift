import Foundation

/// 网络客户端抽象，便于测试替换与模块解耦
protocol NetworkClient {
    func request<T: Decodable>(_ endpoint: APIEndpoint, decode type: T.Type) async throws -> T
    func request(_ endpoint: APIEndpoint) async throws -> Data
    func updateBaseURL(_ value: String)
    func currentBaseURL() -> String
}
