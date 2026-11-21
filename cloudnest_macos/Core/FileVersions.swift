import Foundation
import Observation

struct FileVersionItem: Decodable, Identifiable, Equatable, Sendable {
    let id: Int64
    let version: Int
    let size: Int64?
    let comment: String
    let username: String
    let createTime: String

    var created: Date? {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return formatter.date(from: createTime) ?? ISO8601DateFormatter().date(from: createTime)
    }

    enum CodingKeys: String, CodingKey {
        case id, version, size, comment, username
        case createTime = "create_time"
    }
}

struct FileVersionHistory: Decodable, Sendable {
    let fileID: Int64
    let currentVersion: Int
    let items: [FileVersionItem]

    enum CodingKeys: String, CodingKey {
        case fileID = "file_id"
        case currentVersion = "current_version"
        case items
    }
}

struct RestoredFileVersion: Decodable, Sendable {
    let id: Int64
    let fileID: Int64
    let version: Int
    enum CodingKeys: String, CodingKey {
        case id, version
        case fileID = "file_id"
    }
}

extension ServerAPI {
    private struct RestoreVersionInput: Encodable { let base_version: Int }
    private struct RestoreVersionResponse: Decodable { let item: RestoredFileVersion }

    func fileVersions(_ origin: ServerOrigin, token: String, fileID: Int64) async throws -> FileVersionHistory {
        guard fileID > 0 else { throw ServerError.invalidOperation }
        let history: FileVersionHistory = try await request(origin, "api/files/\(fileID)/versions", token: token,
                                                           maximumBytes: 8 * 1024 * 1024)
        guard history.fileID == fileID, history.currentVersion >= 0,
              history.items.allSatisfy({ $0.id > 0 && $0.version > 0 && $0.version <= history.currentVersion && ($0.size.map { $0 >= 0 } ?? true) }),
              Set(history.items.map(\.id)).count == history.items.count,
              Set(history.items.map(\.version)).count == history.items.count else { throw ServerError.invalidResponse }
        return history
    }

    func restoreVersion(_ origin: ServerOrigin, token: String, fileID: Int64, version: Int,
                        baseVersion: Int) async throws -> RestoredFileVersion {
        guard fileID > 0, version > 0, baseVersion > 0, baseVersion < Int.max else { throw ServerError.invalidOperation }
        let response: RestoreVersionResponse = try await request(origin, "api/files/\(fileID)/versions/\(version)/restore",
            token: token, body: JSONEncoder().encode(RestoreVersionInput(base_version: baseVersion)), method: "POST", transfer: true)
        guard response.item.id > 0, response.item.fileID == fileID, response.item.version == baseVersion + 1 else {
            throw ServerError.invalidResponse
        }
        return response.item
    }
}

@MainActor
@Observable
final class FileVersionState {
    let file: FileItem
    private(set) var items: [FileVersionItem] = []
    private(set) var currentVersion: Int?
    private(set) var busy = false
    private(set) var loaded = false
    private(set) var error: String?
    private(set) var notice: String?
    var selection: Int64?
    private let session: SessionState
    private let identity: TransferIdentity?

    var selected: FileVersionItem? { items.first { $0.id == selection } }

    init(file: FileItem, session: SessionState) {
        self.file = file
        self.session = session
        identity = session.transferIdentity
    }

    func reload() async {
        guard !busy else { return }
        busy = true
        error = nil
        defer { busy = false }
        do { try await readHistory() }
        catch is CancellationError { clearHistory() }
        catch { clearHistory(); self.error = message(error) }
    }

    func restore(_ version: FileVersionItem, expectedCurrentVersion: Int) async -> Bool {
        guard !busy, items.contains(version), version.version != expectedCurrentVersion,
              currentVersion == expectedCurrentVersion else { return false }
        busy = true
        error = nil
        notice = nil
        defer { busy = false }
        do {
            try checkIdentity()
            let restored = try await session.restoreVersion(fileID: file.id, version: version.version,
                                                            baseVersion: expectedCurrentVersion)
            try checkIdentity()
            notice = "已将版本 \(version.version) 恢复为新版本 \(restored.version)。"
            do { try await readHistory() }
            catch { clearHistory(); self.error = "恢复已完成，版本列表暂未刷新。请刷新后继续。" }
            return true
        } catch {
            clearHistory()
            self.error = error as? ServerError == .conflict
                ? "文件或版本内容已变化，请刷新列表并重新确认恢复。"
                : "\(message(error)) 请刷新版本列表，确认请求结果后再操作。"
            return false
        }
    }

    func download(_ version: FileVersionItem, to destination: URL, transfers: TransferManager) async {
        error = nil
        do {
            try checkIdentity()
            guard items.contains(version) else { throw ServerError.invalidOperation }
            try await transfers.enqueueVersionDownload(file, version: version, destination: destination)
            notice = "版本 \(version.version) 已加入下载队列。"
        } catch { self.error = message(error) }
    }

    func upload(_ source: URL, expectedCurrentVersion: Int, comment: String, transfers: TransferManager) async -> UUID? {
        guard !busy else { return nil }
        guard loaded, currentVersion == expectedCurrentVersion else {
            error = "版本列表已变化，请刷新并重新选择上传。"
            return nil
        }
        busy = true
        error = nil
        notice = nil
        defer { busy = false }
        do {
            try checkIdentity()
            let id = try await transfers.enqueueVersionUpload(source, file: file, baseVersion: expectedCurrentVersion, comment: comment)
            try checkIdentity()
            notice = "新版本已加入传输队列，可在侧栏“传输”中暂停、继续或取消。"
            return id
        } catch { self.error = message(error); return nil }
    }

    func observeUpload(_ job: TransferRecord?) async -> Bool {
        guard !busy, let job, job.direction == .versionUpload, job.fileID == file.id,
              job.identity == identity else { return false }
        do { try checkIdentity() } catch { clearHistory(); return false }
        switch job.phase {
        case .completed:
            guard let version = job.publishedVersion else { return false }
            busy = true
            defer { busy = false }
            error = nil
            notice = "已上传为新版本 \(version)，原有历史已保留。"
            do { try await readHistory() }
            catch { clearHistory(); self.error = "上传已完成，版本列表暂未刷新。请刷新后继续。" }
            return true
        case .failed:
            error = job.error
            clearHistory()
        case .cancelled:
            notice = "版本上传已取消。"
        default: break
        }
        return false
    }

    private func readHistory() async throws {
        try checkIdentity()
        let history = try await session.fileVersions(fileID: file.id)
        try Task.checkCancellation()
        try checkIdentity()
        items = history.items.sorted { $0.version > $1.version }
        currentVersion = history.currentVersion
        selection = items.first(where: { $0.id == selection })?.id
        loaded = true
    }

    private func clearHistory() {
        items = []
        selection = nil
        currentVersion = nil
    }

    private func checkIdentity() throws {
        guard identity != nil, session.transferIdentity == identity else { throw ServerError.unauthorized }
    }

    private func message(_ error: any Error) -> String {
        (error as? LocalizedError)?.errorDescription ?? "无法读取或更新文件版本，请检查连接后重试。"
    }
}
