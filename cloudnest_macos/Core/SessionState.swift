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

    private func withSession<T: Sendable>(_ operation: (ServerOrigin, String) async throws -> T) async throws -> T {
        guard let saved, let origin, account != nil else { throw ServerError.unauthorized }
        do {
            return try await operation(origin, saved.token)
        } catch ServerError.unauthorized {
            invalidateSession()
            throw ServerError.unauthorized
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

    func observeTransferFailure(_ error: any Error, identity: TransferIdentity) {
        if error as? ServerError == .unauthorized && transferIdentity == identity { invalidateSession() }
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
