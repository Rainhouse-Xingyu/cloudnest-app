import Foundation

protocol AdminLogServiceProtocol {
    func listOperationLogs(query: OperationLogQueryInput) async throws -> OperationLogListResponseDTO
    func exportOperationLogs(query: OperationLogQueryInput) async throws -> URL
}

final class AdminLogService: AdminLogServiceProtocol {
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

    func listOperationLogs(query: OperationLogQueryInput) async throws -> OperationLogListResponseDTO {
        let endpoint = OperationLogsEndpoint(token: try token, query: query)
        return try await networkClient.request(endpoint, decode: OperationLogListResponseDTO.self)
    }

    func exportOperationLogs(query: OperationLogQueryInput) async throws -> URL {
        let endpoint = OperationLogsExportEndpoint(token: try token, query: query)
        let data = try await networkClient.request(endpoint)
        let folder = try createExportDirectoryIfNeeded()
        let formatter = DateFormatter()
        formatter.dateFormat = "yyyyMMdd_HHmmss"
        let fileURL = folder.appendingPathComponent("operation_logs_\(formatter.string(from: Date())).csv")
        try data.write(to: fileURL, options: .atomic)
        return fileURL
    }

    private func createExportDirectoryIfNeeded() throws -> URL {
        let base = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask).first
            ?? FileManager.default.temporaryDirectory
        let folder = base.appendingPathComponent("AuditExports", isDirectory: true)
        if !FileManager.default.fileExists(atPath: folder.path) {
            try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        }
        return folder
    }
}

private struct OperationLogsEndpoint: APIEndpoint {
    let token: String
    let query: OperationLogQueryInput

    var path: String { "/api/operation-logs" }
    var method: HTTPMethod { .get }
    var queryItems: [URLQueryItem] {
        var items: [URLQueryItem] = [
            URLQueryItem(name: "page", value: "\(query.page)"),
            URLQueryItem(name: "limit", value: "\(query.limit)")
        ]
        if !query.action.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            items.append(URLQueryItem(name: "action", value: query.action.trimmingCharacters(in: .whitespacesAndNewlines)))
        }
        if !query.status.isEmpty {
            items.append(URLQueryItem(name: "status", value: query.status))
        }
        if !query.startTime.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            items.append(URLQueryItem(name: "start_time", value: query.startTime.trimmingCharacters(in: .whitespacesAndNewlines)))
        }
        if !query.endTime.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            items.append(URLQueryItem(name: "end_time", value: query.endTime.trimmingCharacters(in: .whitespacesAndNewlines)))
        }
        if query.includeAll {
            items.append(URLQueryItem(name: "all", value: "1"))
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

private struct OperationLogsExportEndpoint: APIEndpoint {
    let token: String
    let query: OperationLogQueryInput

    var path: String { "/api/operation-logs/export" }
    var method: HTTPMethod { .get }
    var queryItems: [URLQueryItem] {
        var items: [URLQueryItem] = [
            URLQueryItem(name: "limit", value: "\(max(1, min(query.limit, 5000)))")
        ]
        if !query.action.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            items.append(URLQueryItem(name: "action", value: query.action.trimmingCharacters(in: .whitespacesAndNewlines)))
        }
        if !query.status.isEmpty {
            items.append(URLQueryItem(name: "status", value: query.status))
        }
        if !query.startTime.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            items.append(URLQueryItem(name: "start_time", value: query.startTime.trimmingCharacters(in: .whitespacesAndNewlines)))
        }
        if !query.endTime.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            items.append(URLQueryItem(name: "end_time", value: query.endTime.trimmingCharacters(in: .whitespacesAndNewlines)))
        }
        if query.includeAll {
            items.append(URLQueryItem(name: "all", value: "1"))
        }
        return items
    }
    var headers: [String: String] {
        [
            "Authorization": "Bearer \(token)",
            "Accept": "text/csv,application/json"
        ]
    }
}
