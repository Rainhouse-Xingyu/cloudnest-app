import Foundation

struct AuditQuery: Equatable, Sendable {
    let action: String
    let status: String
    let start: Date?
    let end: Date?
    let all: Bool

    init(action: String = "", status: String = "", start: Date? = nil, end: Date? = nil, all: Bool = false) throws {
        let action = action.trimmingCharacters(in: .whitespacesAndNewlines)
        let status = status.trimmingCharacters(in: .whitespacesAndNewlines)
        guard action.utf8.count <= 200, status.utf8.count <= 80,
              action.rangeOfCharacter(from: .controlCharacters) == nil, status.rangeOfCharacter(from: .controlCharacters) == nil,
              start.map({ $0.timeIntervalSince1970.isFinite }) ?? true,
              end.map({ $0.timeIntervalSince1970.isFinite }) ?? true,
              start.flatMap({ start in end.map { $0 >= start } }) ?? true else { throw ServerError.invalidOperation }
        self.action = action; self.status = status
        self.start = start.map { Date(timeIntervalSince1970:floor($0.timeIntervalSince1970)) }
        self.end = end.map { Date(timeIntervalSince1970:floor($0.timeIntervalSince1970)) }; self.all = all
    }

    var parameters: [URLQueryItem] {
        var items = [URLQueryItem(name:"all",value:all ? "1" : "0")]
        if !action.isEmpty { items.append(URLQueryItem(name:"action",value:action)) }
        if !status.isEmpty { items.append(URLQueryItem(name:"status",value:status)) }
        let formatter = ISO8601DateFormatter()
        if let start { items.append(URLQueryItem(name:"start_time",value:formatter.string(from:start))) }
        if let end { items.append(URLQueryItem(name:"end_time",value:formatter.string(from:end))) }
        return items
    }
}

struct AuditEntry: Identifiable, Equatable, Sendable {
    let id: Int64
    let userID: Int64?
    let fileID: Int64?
    let action: String
    let status: String
    let detail: String
    let clientIP: String
    let createdAt: Date
}

struct AuditPage: Sendable {
    let query: AuditQuery
    let items: [AuditEntry]
    let total: Int64
    let snapshotID: Int64
    let nextBeforeID: Int64
    let nextAfterID: Int64
}

struct AuditExportSelection: Equatable, Sendable {
    let query: AuditQuery
    let snapshotID: Int64
    let total: Int64
}

struct AuditExportDescriptor: Sendable {
    static let maximumBytes: Int64 = 16 << 20
    let identity: DownloadIdentity
    let rows: Int
    let truncated: Bool

    init(response: HTTPURLResponse, selection: AuditExportSelection, userID: Int64) throws {
        let identity = try DownloadIdentity(response:response)
        guard identity.length >= Int64("id,user_id,file_id,action,status,detail,client_ip,create_time\n".utf8.count), identity.length <= Self.maximumBytes,
              response.value(forHTTPHeaderField:"Content-Type")?.lowercased() == "text/csv; charset=utf-8",
              response.value(forHTTPHeaderField:"X-CloudNest-Audit-Scope") == (selection.query.all ? "all" : "self"),
              response.value(forHTTPHeaderField:"X-CloudNest-Audit-User").flatMap(Int64.init) == userID,
              response.value(forHTTPHeaderField:"X-CloudNest-Audit-Snapshot").flatMap(Int64.init) == selection.snapshotID,
              response.value(forHTTPHeaderField:"X-CloudNest-Audit-Timezone") == "UTC",
              let rows = response.value(forHTTPHeaderField:"X-CloudNest-Audit-Rows").flatMap(Int.init), (0...5000).contains(rows),
              let flag = response.value(forHTTPHeaderField:"X-CloudNest-Audit-Truncated"), ["true","false"].contains(flag) else { throw ServerError.invalidResponse }
        guard flag == "false" || rows == 5000 else { throw ServerError.invalidResponse }
        self.identity = identity; self.rows = rows; truncated = flag == "true"
    }
}

struct AuditExportStream: Sendable {
    let stream: DownloadStream
    let descriptor: AuditExportDescriptor
}

extension ServerAPI {
    func auditPage(_ origin: ServerOrigin, token: String, userID: Int64, query: AuditQuery, snapshotID: Int64 = 0, beforeID: Int64 = 0, afterID: Int64 = 0) async throws -> AuditPage {
        struct Row: Decodable {
            let id: Int64; let user_id: Int64?; let file_id: Int64?; let action: String; let status: String
            let detail: String; let client_ip: String; let create_time: String
        }
        struct Response: Decodable {
            struct Pagination: Decodable { let limit: Int; let total: Int64 }
            struct Cursor: Decodable { let snapshot_id: Int64; let next_before_id: Int64; let next_after_id: Int64 }
            let items: [Row]?; let scope: String; let user_id: Int64; let pagination: Pagination; let cursor: Cursor
        }
        guard userID > 0, snapshotID >= 0, beforeID >= 0, afterID >= 0, beforeID == 0 || afterID == 0,
              beforeID == 0 && afterID == 0 || snapshotID > 0 else { throw ServerError.invalidOperation }
        var parameters = query.parameters + [URLQueryItem(name:"cursor_mode",value:"1"),URLQueryItem(name:"limit",value:"50")]
        if snapshotID > 0 { parameters.append(URLQueryItem(name:"snapshot_id",value:String(snapshotID))) }
        if beforeID > 0 { parameters.append(URLQueryItem(name:"before_id",value:String(beforeID))) }
        if afterID > 0 { parameters.append(URLQueryItem(name:"after_id",value:String(afterID))) }
        let result: Response = try await request(origin,"api/operation-logs",token:token,query:parameters,maximumBytes:256*1024)
        let rows = result.items ?? [], cursor = result.cursor
        guard result.scope == (query.all ? "all" : "self"), result.user_id == userID, result.pagination.limit == 50,
              rows.count <= 50, result.pagination.total >= rows.count, cursor.snapshot_id >= 0,
              snapshotID == 0 || cursor.snapshot_id == snapshotID,
              cursor.next_before_id >= 0, cursor.next_after_id >= 0,
              cursor.next_before_id == 0 || cursor.next_before_id == rows.last?.id,
              cursor.next_after_id == 0 || cursor.next_after_id == rows.first?.id,
              cursor.snapshot_id > 0 || rows.isEmpty && result.pagination.total == 0,
              snapshotID > 0 || rows.first?.id == cursor.snapshot_id || rows.isEmpty && cursor.snapshot_id == 0 else { throw ServerError.invalidResponse }
        var previous: Int64?
        let entries = try rows.map { row in
            let date = try ShareContract.date(row.create_time)
            guard row.id > 0, previous.map({ row.id < $0 }) ?? true, row.id <= cursor.snapshot_id,
                  beforeID == 0 || row.id < beforeID, afterID == 0 || row.id > afterID,
                  row.user_id.map({ $0 > 0 }) ?? true, row.file_id.map({ $0 > 0 }) ?? true,
                  query.all || row.user_id == userID,
                  query.action.isEmpty || row.action == query.action, query.status.isEmpty || row.status == query.status,
                  query.start.map({ date >= $0 }) ?? true, query.end.map({ date <= $0 }) ?? true,
                  !row.action.isEmpty, row.action.utf8.count <= 200, row.status.utf8.count <= 80,
                  row.action.rangeOfCharacter(from:.controlCharacters) == nil, row.status.rangeOfCharacter(from:.controlCharacters) == nil,
                  row.detail.utf8.count <= 2048, row.client_ip.utf8.count <= 256 else { throw ServerError.invalidResponse }
            previous = row.id
            return AuditEntry(id:row.id,userID:row.user_id,fileID:row.file_id,action:row.action,status:row.status,detail:row.detail,clientIP:row.client_ip,createdAt:date)
        }
        return AuditPage(query:query,items:entries,total:result.pagination.total,snapshotID:cursor.snapshot_id,nextBeforeID:cursor.next_before_id,nextAfterID:cursor.next_after_id)
    }
}
