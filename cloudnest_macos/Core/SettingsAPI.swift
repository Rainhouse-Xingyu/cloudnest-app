import Foundation

enum SystemSettingKey: String, CaseIterable, Identifiable, Sendable {
    case chunkThreshold = "upload_chunk_threshold_mb"
    case chunkSize = "upload_default_chunk_size_mb"
    case shareHours = "share_default_expire_hours"
    case uploadRate = "upload_speed_limit_kbps"
    case downloadRate = "download_speed_limit_kbps"
    case peakUploadRate = "upload_peak_speed_limit_kbps"
    case peakDownloadRate = "download_peak_speed_limit_kbps"
    case peakEnabled = "peak_window_enabled"
    case peakStart = "peak_window_start"
    case peakEnd = "peak_window_end"
    case uploadConcurrency = "upload_max_concurrency"
    case recycleDays = "recycle_retention_days"
    case quotaBytes = "storage_quota_bytes"
    case defaultQuotaBytes = "storage_default_quota_bytes"

    var id: String { rawValue }
    var title: String {
        switch self {
        case .chunkThreshold: "分片上传阈值（MiB）"
        case .chunkSize: "默认分片大小（MiB）"
        case .shareHours: "分享默认有效期（小时）"
        case .uploadRate: "上传速率上限（KiB/s）"
        case .downloadRate: "下载速率上限（KiB/s）"
        case .peakUploadRate: "高峰上传上限（KiB/s）"
        case .peakDownloadRate: "高峰下载上限（KiB/s）"
        case .peakEnabled: "系统高峰时段开关"
        case .peakStart: "系统高峰开始"
        case .peakEnd: "系统高峰结束"
        case .uploadConcurrency: "上传并发数上限"
        case .recycleDays: "回收站保留天数"
        case .quotaBytes: "总存储配额（字节）"
        case .defaultQuotaBytes: "用户默认配额（字节）"
        }
    }
    var guidance: String {
        switch self {
        case .chunkThreshold: "1–10240 MiB"
        case .chunkSize: "1–1024 MiB"
        case .shareHours: "0–8760 小时；0 表示不自动到期"
        case .uploadRate, .downloadRate, .peakUploadRate, .peakDownloadRate: "64–10240 KiB/s；每次传输的上限，高峰只能收紧"
        case .peakEnabled: "0 表示关闭，1 表示开启；开启前请确认开始与结束时间均有效"
        case .peakStart, .peakEnd: "HH:MM，按服务器本地时间；跨午夜有效，相同起止时间表示全天"
        case .uploadConcurrency: "1–32"
        case .recycleDays: "1–3650 天；修改设置不会立即清理文件"
        case .quotaBytes, .defaultQuotaBytes: "1–4611686018427387904 字节；修改配额不会删除已有文件"
        }
    }
    func accepts(_ value: String) -> Bool {
        if self == .peakStart || self == .peakEnd { return SettingsContract.validTime(value) }
        if self == .peakEnabled { return value == "0" || value == "1" }
        guard !value.isEmpty, value.utf8.count <= 19, value.utf8.allSatisfy({ (48...57).contains($0) }), let n = Int64(value) else { return false }
        switch self {
        case .chunkThreshold: return (1...10240).contains(n)
        case .chunkSize: return (1...1024).contains(n)
        case .shareHours: return (0...8760).contains(n)
        case .uploadRate, .downloadRate, .peakUploadRate, .peakDownloadRate: return (64...10240).contains(n)
        case .uploadConcurrency: return (1...32).contains(n)
        case .recycleDays: return (1...3650).contains(n)
        case .quotaBytes, .defaultQuotaBytes: return (1...(1 << 62)).contains(n)
        default: return false
        }
    }
}

struct SystemSetting: Identifiable, Equatable, Sendable {
    let key: SystemSettingKey
    let value: String
    var id: String { key.id }
}

struct SystemSettingsSnapshot: Sendable {
    let items: [SystemSetting]
    let conditionalWrites: Bool
}

struct UserRateWindow: Equatable, Sendable {
    let enabled: Bool
    let start: String
    let end: String
    let source: String
    let expected: [String: String?]?
}

enum SettingsContract {
    static func validTime(_ value: String) -> Bool {
        let bytes = Array(value.utf8)
        guard bytes.count == 5, bytes[2] == 58,
              [0,1,3,4].allSatisfy({ (48...57).contains(bytes[$0]) }) else { return false }
        return (bytes[0]-48)*10 + bytes[1]-48 < 24 && (bytes[3]-48)*10 + bytes[4]-48 < 60
    }
    static func windowKeys(userID: Int64) -> Set<String> {
        let keys = ["peak_window_enabled", "peak_window_start", "peak_window_end"]
        return Set(keys + keys.map { "\($0)_user_\(userID)" })
    }
    static func validate(_ expected: [String: String?], userID: Int64) throws {
        guard userID > 0, Set(expected.keys) == windowKeys(userID: userID),
              expected.values.allSatisfy({ $0.map { $0.utf8.count <= 128 } ?? true }) else { throw ServerError.invalidResponse }
    }
}

extension ServerAPI {
    func systemSettings(_ origin: ServerOrigin, token: String) async throws -> SystemSettingsSnapshot {
        struct Row: Decodable { let setting_key: String; let setting_value: String }
        struct Response: Decodable { let items: [Row]?; let conditional_writes: Bool? }
        let result: Response = try await request(origin, "api/settings", token: token, query: [URLQueryItem(name: "scope", value: "system")], maximumBytes: 64 * 1024)
        let rows = result.items ?? []
        guard rows.count <= SystemSettingKey.allCases.count, Set(rows.map(\.setting_key)).count == rows.count else { throw ServerError.invalidResponse }
        let items = try rows.map { row in
            guard let key = SystemSettingKey(rawValue: row.setting_key), row.setting_value.utf8.count <= 128 else { throw ServerError.invalidResponse }
            return SystemSetting(key: key, value: row.setting_value)
        }
        return SystemSettingsSnapshot(items: items, conditionalWrites: result.conditional_writes == true)
    }

    func saveSystemSetting(_ origin: ServerOrigin, token: String, key: SystemSettingKey, value: String, original: String?) async throws {
        struct Item: Encodable { let setting_key: String; let setting_value: String }
        struct Input: Encodable { let items: [Item]; let expected: [String: String?] }
        guard key.accepts(value), original.map({ $0.utf8.count <= 128 }) ?? true else { throw ServerError.invalidOperation }
        let _: EmptyResponse = try await request(origin, "api/settings", token: token,
            body: JSONEncoder().encode(Input(items: [Item(setting_key: key.rawValue, setting_value: value)], expected: [key.rawValue: original])), method: "PUT")
    }

    func rateWindow(_ origin: ServerOrigin, token: String, userID: Int64) async throws -> UserRateWindow {
        struct Row: Decodable { let enabled: Bool; let start: String; let end: String; let source: String; let expected: [String: String?]? }
        struct Response: Decodable { let item: Row }
        let result: Response = try await request(origin, "api/settings/rate-window", token: token, maximumBytes: 8 * 1024)
        let row = result.item
        guard ["global", "user"].contains(row.source), row.start.utf8.count <= 128, row.end.utf8.count <= 128,
              !row.enabled || SettingsContract.validTime(row.start) && SettingsContract.validTime(row.end) else { throw ServerError.invalidResponse }
        if let expected = row.expected { try SettingsContract.validate(expected, userID: userID) }
        return UserRateWindow(enabled: row.enabled, start: row.start, end: row.end, source: row.source, expected: row.expected)
    }

    func saveRateWindow(_ origin: ServerOrigin, token: String, userID: Int64, original: UserRateWindow, enabled: Bool, start: String, end: String) async throws {
        struct Input: Encodable { let enabled: Bool; let start: String; let end: String; let expected: [String: String?] }
        guard let expected = original.expected, !enabled || SettingsContract.validTime(start) && SettingsContract.validTime(end) else { throw ServerError.invalidOperation }
        try SettingsContract.validate(expected, userID: userID)
        let _: EmptyResponse = try await request(origin, "api/settings/rate-window", token: token,
            body: JSONEncoder().encode(Input(enabled: enabled, start: enabled ? start : "", end: enabled ? end : "", expected: expected)), method: "PUT")
    }
}
