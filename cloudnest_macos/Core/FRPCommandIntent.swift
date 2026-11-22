import Foundation

enum FRPCommandAction: String, Codable, CaseIterable, Sendable {
    case deployConfig = "deploy_config", start, stop, reload, diagnose
}

struct FRPCommandIntent: Codable, Identifiable, Equatable, Sendable {
    enum Outcome: String, Codable, Sendable { case unconfirmed, accepted, abandoned }
    let formatVersion: Int
    let id: UUID
    let identity: TransferIdentity
    let deviceID: Int64
    let action: FRPCommandAction
    let configID: Int64
    let createdAt: Date
    var revision: Int64
    var outcome: Outcome
    var commandID: String?
    var error: String?

    var requestID: String { id.uuidString.replacingOccurrences(of: "-", with: "").lowercased() }

    init(identity: TransferIdentity, deviceID: Int64, action: FRPCommandAction, configID: Int64 = 0) {
        formatVersion = 1
        id = UUID()
        self.identity = identity
        self.deviceID = deviceID
        self.action = action
        self.configID = configID
        createdAt = Date()
        revision = 0
        // A persisted intent may have reached the server before an abrupt exit.
        // Never interpret absence of a receipt as proof that nothing was sent.
        outcome = .unconfirmed
    }

    func sameOperation(as other: Self) -> Bool {
        formatVersion == other.formatVersion && id == other.id && identity == other.identity &&
        deviceID == other.deviceID && action == other.action && configID == other.configID && createdAt == other.createdAt
    }

    func validate() throws {
        guard formatVersion == 1, identity.userID > 0, deviceID > 0,
              (action == .deployConfig ? configID > 0 : configID == 0),
              revision >= 0, revision < Int64.max, createdAt.timeIntervalSince1970.isFinite,
              (0...7_258_118_400).contains(createdAt.timeIntervalSince1970),
              identity.server.utf8.count <= 4096, error.map({ $0.utf8.count <= 1024 }) ?? true else {
            throw FRPCommandStoreError.invalidJournal
        }
        guard let origin = try? ServerOrigin(identity.server), origin.url.absoluteString == identity.server else {
            throw FRPCommandStoreError.invalidJournal
        }
        switch outcome {
        case .unconfirmed, .abandoned:
            guard commandID == nil else { throw FRPCommandStoreError.invalidJournal }
        case .accepted:
            guard let commandID, Self.validID(commandID), error == nil else { throw FRPCommandStoreError.invalidJournal }
        }
    }

    static func validID(_ value: String) -> Bool {
        value.utf8.count == 32 && value.utf8.allSatisfy { (48...57).contains($0) || (97...102).contains($0) }
    }
}

enum FRPCommandStoreError: LocalizedError, Equatable {
    case storage, alreadyRunning, invalidJournal, conflict, closed, full
    var errorDescription: String? {
        switch self {
        case .storage: "无法保存命令记录。请检查本地磁盘后重新打开应用；不要重复创建相同操作。"
        case .alreadyRunning: "另一应用进程正在使用远程命令记录，请先退出该进程。"
        case .invalidJournal: "本地命令记录无法验证，已停止提交。请保留记录以便恢复。"
        case .conflict: "命令记录已变化，请刷新后核对。"
        case .closed: "远程命令记录已关闭。"
        case .full: "远程命令记录达到保留上限，请先归档后再提交新操作。"
        }
    }
}
