import Foundation
import Observation

@MainActor
@Observable
final class FRPCommandManager {
    private(set) var records: [UUID: FRPCommandIntent] = [:]
    private(set) var error: String?
    private(set) var restored = false
    private(set) var closing = false
    private var storageFault = false
    private let session: SessionState
    private let directory: URL
    private var store: FRPCommandStore?
    private var restoration: Task<Void, Never>?
    private var closingTask: Task<Void, Never>?
    private var active: [UUID: Task<UUID, any Error>] = [:]
    private var activeIdentities: [UUID: TransferIdentity] = [:]
    private struct DeviceKey: Hashable { let identity: TransferIdentity; let deviceID: Int64 }
    private var reservedDevices: Set<DeviceKey> = []

    init(session: SessionState, directory: URL = FRPCommandStore.defaultDirectory) {
        self.session = session
        self.directory = directory
    }

    var jobs: [FRPCommandIntent] {
        records.values.filter { $0.identity == session.transferIdentity }.sorted { $0.createdAt > $1.createdAt }
    }

    func busy(deviceID: Int64) -> Bool {
        guard let identity = session.transferIdentity else { return false }
        return reservedDevices.contains(DeviceKey(identity: identity, deviceID: deviceID))
    }

    func restore() async {
        guard !closing, !restored else { return }
        if let restoration { await restoration.value; return }
        let task = Task { await self.loadStore() }
        restoration = task
        await task.value
        restoration = nil
    }

    private func loadStore() async {
        var candidate: FRPCommandStore?
        do {
            let store = try FRPCommandStore(directory: directory)
            candidate = store
            let rows = try await store.load()
            guard !closing else { await store.close(); return }
            self.store = store
            records = Dictionary(uniqueKeysWithValues: rows.map { ($0.id, $0) })
            restored = true
            storageFault = false
            error = nil
            // Never replay remote actions automatically during restoration.
        } catch {
            await candidate?.close()
            self.error = message(error)
        }
    }

    @discardableResult
    func submit(deviceID: Int64, action: FRPCommandAction, configID: Int64 = 0,
                expectedIdentity: TransferIdentity?) async throws -> UUID {
        try session.requireTransferIdentity(expectedIdentity)
        await restore()
        try session.requireTransferIdentity(expectedIdentity)
        try requireStorage()
        let identity = try session.transferContext().identity
        guard !jobs.contains(where: { $0.deviceID == deviceID && $0.action == action && $0.configID == configID && $0.outcome == .unconfirmed }) else {
            throw FRPCommandStoreError.conflict
        }
        let record = FRPCommandIntent(identity: identity, deviceID: deviceID, action: action, configID: configID)
        try record.validate()
        return try await run(record) { try await self.persistAndSend(record, inserting: true) }
    }

    @discardableResult
    func retry(_ id: UUID) async throws -> UUID {
        await restore()
        try requireStorage()
        guard let record = records[id], record.outcome == .unconfirmed else { throw ServerError.invalidOperation }
        try session.requireTransferIdentity(record.identity)
        return try await run(record) { try await self.persistAndSend(record, inserting: false) }
    }

    // This only stops local retries. It does not cancel an accepted remote job.
    func abandon(_ id: UUID) async throws {
        await restore()
        try requireStorage()
        guard var record = records[id], record.outcome == .unconfirmed, active[id] == nil else {
            throw ServerError.invalidOperation
        }
        try session.requireTransferIdentity(record.identity)
        record.revision += 1
        record.outcome = .abandoned
        record.error = "已停止本地重试；服务器可能已接收此命令，请核对命令记录。"
        let changed = record
        _ = try await run(changed) {
            try Task.checkCancellation()
            guard let store = self.store else { throw FRPCommandStoreError.storage }
            try await store.replace(changed)
            self.records[id] = changed
            return id
        }
    }

    private func run(_ record: FRPCommandIntent, operation: @escaping @MainActor () async throws -> UUID) async throws -> UUID {
        try requireStorage()
        if let existing = active[record.id] { return try await existing.value }
        let key = DeviceKey(identity: record.identity, deviceID: record.deviceID)
        guard reservedDevices.insert(key).inserted else { throw FRPCommandStoreError.conflict }
        let task = Task { try await operation() }
        active[record.id] = task
        activeIdentities[record.id] = record.identity
        defer { active[record.id] = nil; activeIdentities[record.id] = nil; reservedDevices.remove(key) }
        return try await withTaskCancellationHandler { try await task.value } onCancel: { task.cancel() }
    }

    private func persistAndSend(_ original: FRPCommandIntent, inserting: Bool) async throws -> UUID {
        guard let store else { throw FRPCommandStoreError.storage }
        try session.requireTransferIdentity(original.identity)
        if inserting {
            do { try await store.insert(original) }
            catch {
                if error as? FRPCommandStoreError == .storage { storageFault = true }
                self.error = message(error)
                throw error
            }
            records[original.id] = original
        }
        let command: FRPCommand
        do {
            try Task.checkCancellation()
            guard !closing else { throw FRPCommandStoreError.closed }
            try session.requireTransferIdentity(original.identity)
            command = try await session.enqueueFRPCommand(original)
        } catch {
            var uncertain = original
            uncertain.revision += 1
            uncertain.error = "结果未确认。\(message(error))请使用原请求重试，或核对服务器命令记录。"
            do {
                try await store.replace(uncertain)
                records[uncertain.id] = uncertain
            } catch {
                storageFault = true
                self.error = message(error)
                throw error
            }
            return original.id
        }
        var accepted = original
        accepted.revision += 1
        accepted.outcome = .accepted
        accepted.commandID = command.id
        accepted.error = nil
        do {
            try await store.replace(accepted)
            records[accepted.id] = accepted
            if session.transferIdentity == original.identity && !storageFault { error = nil }
        } catch {
            // The positive server receipt remains true even if local saving
            // fails. The prior unconfirmed journal still has the original key.
            records[accepted.id] = accepted
            storageFault = true
            self.error = "服务器已接收命令，但本地回执保存失败。请保留记录并重新打开应用核对。"
            throw error
        }
        return original.id
    }

    func close() async {
        if let closingTask { await closingTask.value; return }
        closing = true
        let task = Task {
            let tasks = Array(self.active.values)
            for task in tasks { task.cancel() }
            for task in tasks { _ = try? await task.value }
            await self.restoration?.value
            await self.store?.close()
            self.store = nil
        }
        closingTask = task
        await task.value
    }

    func synchronizeIdentity() {
        for (id, task) in active where activeIdentities[id] != session.transferIdentity { task.cancel() }
    }

    private func requireStorage() throws {
        guard !closing else { throw FRPCommandStoreError.closed }
        guard !storageFault, restored, store != nil else { throw FRPCommandStoreError.storage }
    }

    private func message(_ error: any Error) -> String {
        if error as? ServerError == .conflict { return "设备未注册、凭据已更新或存在命令冲突。请刷新后核对。" }
        if error as? ServerError == .notFound { return "设备或配置已不可用，请刷新后核对。" }
        if let error = error as? ServerError { return error.localizedDescription }
        if let error = error as? FRPCommandStoreError { return error.localizedDescription }
        if error is CancellationError { return "请求已停止。" }
        return "连接未能完成。"
    }
}
