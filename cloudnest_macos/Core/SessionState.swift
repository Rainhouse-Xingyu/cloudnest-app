import Foundation
import Observation

@MainActor
@Observable
final class SessionState {
    enum Connection: Equatable {
        case unchecked, checking, connected, restricted, unavailable
    }

    private(set) var account: Account?
    private(set) var origin: ServerOrigin?
    private(set) var connection: Connection = .unchecked
    private(set) var busy = false
    private(set) var error: String?
    private(set) var restored = false
    private var saved: SavedSession?
    private let api: ServerAPI
    private let vault: any SessionVault

    var hasSavedSession: Bool { saved != nil }

    init(api: ServerAPI, vault: any SessionVault) {
        self.api = api
        self.vault = vault
    }

    func restore() async {
        guard !restored, !busy else { return }
        busy = true
        error = nil
        defer { busy = false }
        do {
            saved = try vault.read()
            restored = true
            guard let saved else { return }
            let origin = try ServerOrigin(saved.server)
            self.origin = origin
            connection = .checking
            account = try await api.profile(origin, token: saved.token)
            try await checkHealth(origin, token: saved.token)
        } catch ServerError.unauthorized {
            invalidateSession()
        } catch {
            connection = .unavailable
            self.error = message(error)
        }
    }

    func signIn(server: String, username: String, password: String) async {
        guard restored, saved == nil, !busy else { return }
        busy = true
        error = nil
        defer { busy = false }
        do {
            let origin = try ServerOrigin(server)
            let result = try await api.login(origin, username: username, password: password)
            let session = SavedSession(server: origin.url.absoluteString, token: result.token)
            do {
                try vault.write(session)
            } catch {
                // Best effort revocation of a token that could not be stored locally.
                try? await api.logout(origin, token: result.token)
                throw error
            }
            saved = session
            self.origin = origin
            account = result.user
            connection = .checking
            try await checkHealth(origin, token: session.token)
        } catch ServerError.unauthorized {
            invalidateSession()
        } catch {
            connection = .unavailable
            self.error = message(error)
        }
    }

    func refresh() async {
        guard let saved, let origin, !busy else { return }
        busy = true
        error = nil
        connection = .checking
        defer { busy = false }
        do {
            account = try await api.profile(origin, token: saved.token)
            try await checkHealth(origin, token: saved.token)
        } catch ServerError.unauthorized {
            invalidateSession()
        } catch {
            connection = .unavailable
            self.error = message(error)
        }
    }

    func signOut() async {
        guard let saved, let origin, !busy else { return }
        busy = true
        error = nil
        defer { busy = false }
        do {
            do {
                try await api.logout(origin, token: saved.token)
            } catch ServerError.unauthorized {
                // The server has already invalidated this session.
            }
            clearSession()
        } catch {
            connection = .unavailable
            self.error = message(error)
        }
    }

    func listFiles(parentID: Int64, recycleBin: Bool) async throws -> [FileItem] {
        try await withSession { origin, token in
            try await api.files(origin, token: token, parentID: parentID, recycleBin: recycleBin)
        }
    }

    func createFolder(name: String, parentID: Int64) async throws {
        try await withSession { origin, token in
            _ = try await api.createFolder(origin, token: token, name: name, parentID: parentID)
        }
    }

    func renameFile(id: Int64, name: String) async throws {
        try await withSession { origin, token in
            try await api.renameFile(origin, token: token, id: id, name: name)
        }
    }

    func trashFiles(ids: [Int64]) async throws -> DeleteFilesResponse {
        try await withSession { origin, token in
            try await api.trashFiles(origin, token: token, ids: ids)
        }
    }

    func restoreFile(id: Int64) async throws {
        try await withSession { origin, token in
            try await api.restoreFile(origin, token: token, id: id)
        }
    }

    func fileVersions(fileID: Int64, before: Int? = nil, after: Int? = nil, snapshot: Int? = nil) async throws -> FileVersionHistory {
        try await withSession { origin, token in
            try await api.fileVersions(origin, token: token, fileID: fileID, before: before, after: after, snapshot: snapshot)
        }
    }

    func restoreVersion(fileID: Int64, version: Int, baseVersion: Int) async throws -> RestoredFileVersion {
        try await withSession { origin, token in
            try await api.restoreVersion(origin, token: token, fileID: fileID, version: version, baseVersion: baseVersion)
        }
    }

    func searchAI(question: String, page: Int) async throws -> AISearchResponse {
        try await withSession { origin, token in
            try await api.searchAI(origin, token: token, question: question, page: page)
        }
    }

    func indexAI(after: Int64) async throws -> AIIndexPage {
        try await withSession { origin, token in
            try await api.indexAI(origin, token: token, after: after)
        }
    }

    func rebuildAI(_ item: AIIndexItem) async throws {
        try await withSession { origin, token in
            try await api.rebuildAI(origin, token: token, item: item)
        }
    }

    func frpDevices(retired: Bool = false) async throws -> [FRPDevice] {
        try await withSession { origin, token in try await api.frpDevices(origin, token: token, retired: retired) }
    }

    func writeFRPDevice(original: FRPDevice?, name: String, address: String, enabled: Bool, expectedIdentity: TransferIdentity?) async throws -> FRPDevice {
        try requireTransferIdentity(expectedIdentity)
        return try await withSession { origin, token in try await api.writeFRPDevice(origin, token: token, original: original, name: name, address: address, enabled: enabled) }
    }

    func retireFRPDevice(id: Int64, expectedIdentity: TransferIdentity?) async throws {
        try requireTransferIdentity(expectedIdentity)
        try await withSession { origin, token in try await api.retireFRPDevice(origin, token: token, id: id) }
    }

    func frpDeviceArchive(id: Int64) async throws -> FRPDeviceArchive {
        try await withSession { origin, token in try await api.frpDeviceArchive(origin, token: token, id: id) }
    }

    func frpMappings(deviceID: Int64) async throws -> [FRPMapping] {
        try await withSession { origin, token in try await api.frpMappings(origin, token: token, deviceID: deviceID) }
    }

    func writeFRPMapping(original: FRPMapping?, deviceID: Int64, serverID: Int64, localPort: Int, remotePort: Int?, domain: String, enabled: Bool, expectedIdentity: TransferIdentity?) async throws -> FRPMapping {
        try requireTransferIdentity(expectedIdentity)
        return try await withSession { origin, token in
            try await api.writeFRPMapping(origin, token: token, original: original, deviceID: deviceID,
                serverID: serverID, localPort: localPort, remotePort: remotePort, domain: domain, enabled: enabled)
        }
    }

    func deleteFRPMapping(id: Int64, expectedIdentity: TransferIdentity?) async throws {
        try requireTransferIdentity(expectedIdentity)
        try await withSession { origin, token in try await api.deleteFRPMapping(origin, token: token, id: id) }
    }

    func frpStatus(deviceID: Int64) async throws -> FRPStatus {
        try await withSession { origin, token in try await api.frpStatus(origin, token: token, deviceID: deviceID) }
    }

    func frpConfigs(deviceID: Int64) async throws -> [FRPConfig] {
        try await withSession { origin, token in try await api.frpConfigs(origin, token: token, deviceID: deviceID) }
    }

    func frpCommands(deviceID: Int64) async throws -> [FRPCommand] {
        try await withSession { origin, token in try await api.frpCommands(origin, token: token, deviceID: deviceID) }
    }

    func enqueueFRPCommand(_ intent: FRPCommandIntent) async throws -> FRPCommand {
        try requireTransferIdentity(intent.identity)
        return try await withSession { origin, token in try await api.enqueueFRPCommand(origin, token: token, intent: intent) }
    }


    func frpServers(afterID: Int64 = 0) async throws -> FRPServerPage {
        try await withSession { origin, token in try await api.frpServers(origin, token: token, afterID: afterID) }
    }

    func frpServer(id: Int64) async throws -> FRPServer {
        try await withSession { origin, token in try await api.frpServer(origin, token: token, id: id) }
    }

    func writeFRPServer(id: Int64?, input: FRPServerInput, expectedIdentity: TransferIdentity?) async throws -> FRPServer {
        try requireTransferIdentity(expectedIdentity)
        return try await withSession { origin, token in try await api.writeFRPServer(origin, token: token, id: id, input: input) }
    }

    func previewFRPConfig(_ selection: FRPConfigSelection, expectedIdentity: TransferIdentity?) async throws -> String {
        try requireTransferIdentity(expectedIdentity)
        return try await withSession { origin, token in try await api.previewFRPConfig(origin, token: token, selection: selection) }
    }

    func saveFRPConfig(_ selection: FRPConfigSelection, expectedIdentity: TransferIdentity?) async throws -> FRPConfig {
        try requireTransferIdentity(expectedIdentity)
        return try await withSession { origin, token in try await api.saveFRPConfig(origin, token: token, selection: selection) }
    }

    func rollbackFRPConfig(configID: Int64, deviceID: Int64, expectedIdentity: TransferIdentity?) async throws -> FRPConfig {
        try requireTransferIdentity(expectedIdentity)
        return try await withSession { origin, token in try await api.rollbackFRPConfig(origin, token: token, configID: configID, deviceID: deviceID) }
    }

    func issueFRPEnrollment(deviceID: Int64, password: String, expectedIdentity: TransferIdentity?) async throws -> FRPEnrollment {
        try requireTransferIdentity(expectedIdentity)
        return try await withSession { origin, token in try await api.issueFRPEnrollment(origin, token: token, deviceID: deviceID, password: password) }
    }

    func revokeFRPCredential(deviceID: Int64, password: String, expectedIdentity: TransferIdentity?) async throws {
        try requireTransferIdentity(expectedIdentity)
        try await withSession { origin, token in try await api.revokeFRPCredential(origin, token: token, deviceID: deviceID, password: password) }
    }

    func workspaces() async throws -> [Workspace] {
        try await withSession { origin, token in try await api.workspaces(origin, token: token) }
    }

    func createWorkspace(name: String, expectedIdentity: TransferIdentity?) async throws -> Workspace {
        try requireTransferIdentity(expectedIdentity)
        guard let ownerID = expectedIdentity?.userID else { throw ServerError.unauthorized }
        return try await withSession { origin, token in try await api.createWorkspace(origin, token: token, name: name, expectedOwnerID: ownerID) }
    }

    func workspaceMembers(workspaceID: Int64) async throws -> [WorkspaceMember] {
        try await withSession { origin, token in try await api.workspaceMembers(origin, token: token, workspaceID: workspaceID) }
    }

    func addWorkspaceMember(workspaceID: Int64, username: String, role: WorkspaceRole, expectedIdentity: TransferIdentity?) async throws {
        try requireTransferIdentity(expectedIdentity)
        try await withSession { origin, token in try await api.addWorkspaceMember(origin, token: token, workspaceID: workspaceID, username: username, role: role) }
    }

    func changeWorkspaceMember(workspaceID: Int64, original: WorkspaceMember, role: WorkspaceRole?, expectedIdentity: TransferIdentity?) async throws {
        try requireTransferIdentity(expectedIdentity)
        try await withSession { origin, token in try await api.changeWorkspaceMember(origin, token: token, workspaceID: workspaceID, original: original, role: role) }
    }

    func fileCollaborators(fileID: Int64) async throws -> [FileCollaborator] {
        try await withSession { origin, token in try await api.fileCollaborators(origin, token: token, fileID: fileID) }
    }

    func addFileCollaborator(file: FileItem, username: String, permission: FilePermission, expectedIdentity: TransferIdentity?) async throws {
        try requireTransferIdentity(expectedIdentity)
        guard file.canManageCollaborators(userID: expectedIdentity?.userID) else { throw ServerError.forbidden }
        try await withSession { origin, token in try await api.addFileCollaborator(origin, token: token, fileID: file.id, username: username, permission: permission) }
    }

    func changeFileCollaborator(file: FileItem, original: FileCollaborator, permission: FilePermission?, expectedIdentity: TransferIdentity?) async throws {
        try requireTransferIdentity(expectedIdentity)
        guard file.canManageCollaborators(userID: expectedIdentity?.userID) else { throw ServerError.forbidden }
        try await withSession { origin, token in try await api.changeFileCollaborator(origin, token: token, fileID: file.id, original: original, permission: permission) }
    }

    private func withSession<T: Sendable>(_ operation: (ServerOrigin, String) async throws -> T) async throws -> T {
        guard let saved, let origin, account != nil else { throw ServerError.unauthorized }
        do {
            let result = try await operation(origin, saved.token)
            guard self.saved == saved else { throw CancellationError() }
            return result
        } catch {
            // A request from a logged-out session must never update or revoke
            // the account that replaced it, including a new login to this user.
            guard self.saved == saved else { throw CancellationError() }
            if error as? ServerError == .unauthorized { invalidateSession() }
            throw error
        }
    }

    func relocateFiles(ids: [Int64], parentID: Int64, copy: Bool) async throws {
        try await withSession { origin, token in
            try await api.relocateFiles(origin, token: token, ids: ids, parentID: parentID, copy: copy)
        }
    }

    var transferIdentity: TransferIdentity? {
        guard let origin, let account, saved != nil else { return nil }
        return TransferIdentity(server: origin.url.absoluteString, userID: account.id)
    }

    func transferContext() throws -> TransferContext {
        guard let identity = transferIdentity, let origin, let saved else { throw ServerError.unauthorized }
        return TransferContext(identity: identity, origin: origin, token: saved.token, api: api)
    }

    func requireTransferIdentity(_ expected: TransferIdentity?) throws {
        guard let expected, let current = transferIdentity else { throw ServerError.unauthorized }
        guard expected == current else { throw ServerError.sessionChanged }
    }

    func observeTransferFailure(_ error: any Error, context: TransferContext) {
        if error as? ServerError == .unauthorized, transferIdentity == context.identity,
           saved?.token == context.token { invalidateSession() }
    }

    private func checkHealth(_ origin: ServerOrigin, token: String) async throws {
        do {
            try await api.health(origin, token: token)
            connection = .connected
        } catch ServerError.forbidden {
            // Custom permissions may deny health without revoking the session.
            connection = .restricted
        }
    }

    private func invalidateSession() {
        clearSession()
        if error == nil { error = ServerError.unauthorized.localizedDescription }
    }

    private func clearSession() {
        account = nil
        connection = .unchecked
        do {
            try vault.clear()
            saved = nil
        } catch {
            self.error = message(error)
        }
    }

    private func message(_ error: any Error) -> String {
        if let error = error as? ServerError { return error.localizedDescription }
        if let error = error as? KeychainError { return error.localizedDescription }
        return "无法完成连接。请检查网络和服务器证书后重试。"
    }
}
