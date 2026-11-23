import Foundation
import Observation

@MainActor @Observable
final class WorkspaceBrowserState {
    private let session: SessionState
    private let identity: TransferIdentity?
    private var request: Task<Void, Never>?
    private var generation = UUID()
    private(set) var items: [Workspace] = []
    private(set) var busy = false
    private(set) var loaded = false
    private(set) var error: String?

    init(session: SessionState) { self.session = session; identity = session.transferIdentity }

    func load() async {
        request?.cancel()
        let id = UUID(); generation = id
        busy = true; error = nil
        let task = Task {
            defer { if generation == id { busy = false } }
            do {
                try session.requireTransferIdentity(identity)
                let rows = try await session.workspaces()
                try Task.checkCancellation()
                try session.requireTransferIdentity(identity)
                guard generation == id else { return }
                items = rows; loaded = true
            } catch is CancellationError { return }
            catch {
                guard generation == id else { return }
                items = []; loaded = true
                self.error = (error as? ServerError)?.localizedDescription ?? "无法读取协作空间，请检查连接。"
            }
        }
        request = task; await task.value
    }

    func cancel() { generation = UUID(); request?.cancel(); request = nil; busy = false; items = [] }
}

@MainActor @Observable
final class WorkspaceMembersState {
    let workspace: Workspace
    private let session: SessionState
    private let identity: TransferIdentity?
    private var request: Task<Void, Never>?
    private var generation = UUID()
    private(set) var members: [WorkspaceMember] = []
    private(set) var busy = false
    private(set) var error: String?

    init(session: SessionState, workspace: Workspace) {
        self.session = session; self.workspace = workspace; identity = session.transferIdentity
    }

    func load() async {
        request?.cancel()
        let id = UUID(); generation = id
        busy = true; error = nil
        let task = Task {
            defer { if generation == id { busy = false } }
            do {
                try session.requireTransferIdentity(identity)
                let rows = try await session.workspaceMembers(workspaceID: workspace.id)
                try Task.checkCancellation()
                try session.requireTransferIdentity(identity)
                guard generation == id else { return }
                members = rows
            } catch is CancellationError { return }
            catch {
                guard generation == id else { return }
                members = []
                self.error = (error as? ServerError)?.localizedDescription ?? "无法读取成员，请检查连接。"
            }
        }
        request = task; await task.value
    }

    func cancel() { generation = UUID(); request?.cancel(); request = nil; busy = false; members = [] }
}
