import Foundation
import Observation

@MainActor
@Observable
final class FRPBrowserState {
    private(set) var devices: [FRPDevice] = []
    private(set) var busy = false
    private(set) var loaded = false
    private(set) var error: String?
    var filter = ""
    private let session: SessionState
    private let identity: TransferIdentity?
    private var revision = UUID()
    private var request: Task<[FRPDevice], any Error>?

    init(session: SessionState) {
        self.session = session
        identity = session.transferIdentity
    }

    var visibleDevices: [FRPDevice] {
        devices.filter { filter.isEmpty || $0.name.localizedStandardContains(filter) || $0.address.localizedStandardContains(filter) }
    }

    func cancel() { revision = UUID(); request?.cancel(); request = nil; busy = false }

    func reload(retired: Bool = false) async {
        cancel()
        let revision = self.revision
        busy = true
        loaded = false
        devices = []
        error = nil
        let task = Task { try session.requireTransferIdentity(identity); return try await session.frpDevices(retired: retired) }
        request = task
        defer { if self.revision == revision { busy = false; loaded = true; request = nil } }
        do {
            let devices = try await withTaskCancellationHandler { try await task.value } onCancel: { task.cancel() }
            try Task.checkCancellation()
            guard self.revision == revision else { return }
            try session.requireTransferIdentity(identity)
            self.devices = devices
        } catch {
            guard self.revision == revision, !task.isCancelled, !(error is CancellationError) else { return }
            self.error = (error as? ServerError)?.localizedDescription ?? "无法读取设备，请检查连接后重试。"
        }
    }
}

enum FRPSection: String, CaseIterable, Sendable {
    case status = "运行状态", mappings = "映射", configs = "配置历史", commands = "命令记录"
}

enum FRPDetailSnapshot: Sendable {
    case status(FRPStatus), mappings([FRPMapping]), configs([FRPConfig]), commands([FRPCommand])
}

@MainActor
@Observable
final class FRPDetailState {
    let device: FRPDevice
    private(set) var snapshot: FRPDetailSnapshot?
    private(set) var busy = false
    private(set) var error: String?
    private(set) var checkedAt: Date?
    private var revision = UUID()
    private var request: Task<FRPDetailSnapshot, any Error>?
    private let session: SessionState
    private let identity: TransferIdentity?

    init(device: FRPDevice, session: SessionState) {
        self.device = device
        self.session = session
        identity = session.transferIdentity
    }

    func cancel() {
        revision = UUID()
        request?.cancel()
        request = nil
        busy = false
    }

    func load(_ section: FRPSection) async {
        cancel()
        let revision = self.revision
        busy = true
        snapshot = nil
        checkedAt = nil
        error = nil
        let task = Task<FRPDetailSnapshot, any Error> {
            try session.requireTransferIdentity(identity)
            try Task.checkCancellation()
            switch section {
            case .status: return .status(try await session.frpStatus(deviceID: device.id))
            case .mappings: return .mappings(try await session.frpMappings(deviceID: device.id))
            case .configs: return .configs(try await session.frpConfigs(deviceID: device.id))
            case .commands: return .commands(try await session.frpCommands(deviceID: device.id))
            }
        }
        request = task
        defer { if self.revision == revision { busy = false; request = nil } }
        do {
            let value = try await withTaskCancellationHandler { try await task.value } onCancel: { task.cancel() }
            try Task.checkCancellation()
            guard self.revision == revision else { return }
            try session.requireTransferIdentity(identity)
            snapshot = value
            checkedAt = Date()
        } catch {
            guard self.revision == revision, !task.isCancelled, !(error is CancellationError) else { return }
            snapshot = nil
            self.error = (error as? ServerError)?.localizedDescription ?? "无法读取设备信息，请检查连接后重试。"
        }
    }
}
