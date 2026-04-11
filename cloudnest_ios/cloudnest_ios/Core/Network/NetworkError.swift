import Foundation

/// 网络层统一错误定义
enum NetworkError: Error, LocalizedError {
    case invalidURL
    case invalidResponse
    case httpStatus(Int, Data?)
    case decoding(Error)
    case transport(Error)

    var errorDescription: String? {
        switch self {
        case .invalidURL:
            return "无效的请求地址"
        case .invalidResponse:
            return "服务响应格式错误"
        case let .httpStatus(code, data):
            if
                let data,
                let payload = try? JSONDecoder().decode(ErrorResponse.self, from: data),
                !payload.error.isEmpty
            {
                return payload.error
            }
            return "服务请求失败，状态码: \(code)"
        case let .decoding(error):
            return "数据解析失败: \(error.localizedDescription)"
        case let .transport(error):
            return "网络传输失败: \(error.localizedDescription)"
        }
    }
}

/// 后端错误响应体（通用）
private struct ErrorResponse: Decodable {
    let error: String
}
