import Foundation

/// 审计日志行
struct OperationLogItemDTO: Decodable, Identifiable, Equatable {
    let id: Int64
    let userId: Int64?
    let fileId: Int64?
    let action: String
    let status: String
    let detail: String
    let clientIP: String
    let createTime: String

    enum CodingKeys: String, CodingKey {
        case id
        case userId = "user_id"
        case fileId = "file_id"
        case action
        case status
        case detail
        case clientIP = "client_ip"
        case createTime = "create_time"
    }
}

struct OperationLogListResponseDTO: Decodable {
    let items: [OperationLogItemDTO]
    let pagination: OperationLogPaginationDTO
}

struct OperationLogPaginationDTO: Decodable {
    let page: Int
    let limit: Int
    let total: Int64
}

/// 日志查询条件
struct OperationLogQueryInput {
    var action: String = ""
    var status: String = ""
    var startTime: String = ""
    var endTime: String = ""
    var page: Int = 1
    var limit: Int = 30
    var includeAll: Bool = false
}

enum OperationLogStatusFilter: String, CaseIterable, Identifiable {
    case all = ""
    case success = "success"
    case failed = "failed"

    var id: String { rawValue }

    var title: String {
        switch self {
        case .all:
            return "全部"
        case .success:
            return "成功"
        case .failed:
            return "失败"
        }
    }
}
