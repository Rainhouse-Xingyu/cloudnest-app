import Foundation
import Observation

@MainActor @Observable
final class FileCollaboratorsState {
    let file: FileItem
    private let session: SessionState
    private let identity: TransferIdentity?
    private var request: Task<Void, Never>?
    private var generation = UUID()
    private(set) var items: [FileCollaborator] = []
    private(set) var busy = false
    private(set) var loaded = false
    private(set) var error: String?

    var canManage: Bool { file.canManageCollaborators(userID: identity?.userID) }

    init(session: SessionState, file: FileItem) {
        self.session = session; self.file = file; identity = session.transferIdentity
    }

    func load() async {
        request?.cancel()
        let id = UUID(); generation = id
        busy = true; error = nil
        let task = Task {
            defer { if generation == id { busy = false } }
            do {
                try session.requireTransferIdentity(identity)
                let rows = try await session.fileCollaborators(fileID: file.id)
                try Task.checkCancellation()
                try session.requireTransferIdentity(identity)
                guard generation == id else { return }
                items = rows; loaded = true
            } catch is CancellationError { return }
            catch {
                guard generation == id else { return }
                items = []; loaded = true
                self.error = (error as? ServerError)?.localizedDescription ?? "无法读取文件协作者，请检查连接。"
            }
        }
        request = task; await task.value
    }

    func cancel() { generation = UUID(); request?.cancel(); request = nil; busy = false; items = [] }
}
