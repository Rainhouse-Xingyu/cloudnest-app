import Foundation
import Testing
@testable import CloudNestCore

private final class CollaborationTestVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ value: SavedSession) throws { saved = value }
    func clear() throws { saved = nil }
}

extension RealBackendTests {
    @Test(.enabled(if: ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_COLLABORATION_TEST"] == "1"))
    @MainActor func realBackendFileCollaboration() async throws {
        let env = ProcessInfo.processInfo.environment
        let origin = try ServerOrigin(#require(env["CLOUDNEST_SWIFT_ORIGIN"]))
        let certificate = try #require(env["CLOUDNEST_SWIFT_CERTIFICATE"])
        let delegate = try FixtureTLSDelegate(der: Data(contentsOf: URL(fileURLWithPath: certificate)))
        let transport = URLSession(configuration: .ephemeral, delegate: delegate, delegateQueue: nil)
        defer { transport.invalidateAndCancel() }
        let api = ServerAPI(session: transport, delegate: delegate)
        let owner = SessionState(api: api, vault: CollaborationTestVault())
        let password = try #require(env["CLOUDNEST_SWIFT_PASSWORD"])
        await owner.restore()
        await owner.signIn(server: origin.url.absoluteString, username: try #require(env["CLOUDNEST_SWIFT_USERNAME"]), password: password)
        let identity = try #require(owner.transferIdentity)
        let fileText = try #require(env["CLOUDNEST_SWIFT_COLLABORATION_FILE"])
        let fileID = try #require(Int64(fileText))
        if env["CLOUDNEST_SWIFT_COLLABORATION_ROLE"] == "restricted" {
            do {
                _ = try await owner.fileCollaborators(fileID: fileID)
                Issue.record("Restricted account could read collaborator metadata")
            } catch { #expect(error as? ServerError == .forbidden) }
            #expect(owner.account != nil)
            return
        }
        let files = try await owner.listFiles(parentID: 0, recycleBin: false)
        let file = try #require(files.first { $0.id == fileID })
        #expect(file.userID == identity.userID && file.workspaceID == nil && file.canManageCollaborators(userID: identity.userID))
        let readerName = try #require(env["CLOUDNEST_SWIFT_COLLABORATION_READER"])
        try await owner.addFileCollaborator(file: file, username: readerName, permission: .read, expectedIdentity: identity)
        do {
            try await owner.addFileCollaborator(file: file, username: readerName, permission: .write, expectedIdentity: identity)
            Issue.record("Create-only grant overwrote existing read access")
        } catch { #expect(error as? ServerError == .conflict) }
        let readerVault = CollaborationTestVault()
        let reader = SessionState(api: api, vault: readerVault)
        await reader.restore()
        await reader.signIn(server: origin.url.absoluteString, username: readerName, password: password)
        let readerIdentity = try #require(reader.transferIdentity)
        let readerFiles = try await reader.listFiles(parentID: 0, recycleBin: false)
        let readable = try #require(readerFiles.first { $0.id == file.id })
        #expect(!readable.canManageCollaborators(userID: readerIdentity.userID))
        let state = FileCollaboratorsState(session: reader, file: readable)
        await state.load()
        #expect(!state.canManage && state.items.count == 1 && state.items.first?.permission == .read)
        let directory = FileManager.default.temporaryDirectory.appending(path: "cloudnest-collaboration-\(UUID())")
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        defer { try? FileManager.default.removeItem(at: directory) }
        let manager = TransferManager(session: reader, directory: directory.appending(path: "journal"))
        let target = directory.appending(path: "Read.txt")
        let readJob = try await manager.enqueueDownload(readable, destination: target, expectedIdentity: readerIdentity)
        await manager.waitUntilIdle(readJob)
        #expect(manager.records[readJob]?.phase == .completed)
        #expect(try Data(contentsOf: target) == Data("Native file collaboration download.\n".utf8))
        do {
            try await reader.renameFile(id: file.id, name: "Read denied.txt")
            Issue.record("Read-only collaborator renamed a file")
        } catch { #expect(error as? ServerError == .forbidden) }
        let before = try await owner.fileCollaborators(fileID: file.id)
        let original = try #require(before.first { $0.userID == readerIdentity.userID })
        do {
            let other = FileItem(id: file.id + 100000, filename: file.filename, parentID: 0, isFolder: 0, size: file.size,
                currentVersion: file.currentVersion, updateTime: file.updateTime, fileType: file.fileType, userID: identity.userID)
            try await owner.changeFileCollaborator(file: other, original: original, permission: .write, expectedIdentity: identity)
            Issue.record("Grant was submitted under a different file selection")
        } catch { #expect(error as? ServerError == .invalidOperation) }
        try await owner.changeFileCollaborator(file: file, original: original, permission: .write, expectedIdentity: identity)
        for permission in [FilePermission.write, nil] as [FilePermission?] {
            do {
                try await owner.changeFileCollaborator(file: file, original: original, permission: permission, expectedIdentity: identity)
                Issue.record("Stale grant update or revoke overwrote a newer permission")
            } catch { #expect(error as? ServerError == .conflict) }
        }
        try await reader.renameFile(id: file.id, name: "Collaborator renamed.txt")
        let third = try #require(env["CLOUDNEST_SWIFT_COLLABORATION_THIRD"])
        do {
            try await reader.addFileCollaborator(file: readable, username: third, permission: .read, expectedIdentity: readerIdentity)
            Issue.record("Writer could delegate from the native session")
        } catch { #expect(error as? ServerError == .forbidden) }
        do {
            try await api.addFileCollaborator(origin, token: try #require(readerVault.read()?.token), fileID: file.id, username: third, permission: .read)
            Issue.record("Writer could delegate on the server")
        } catch { #expect(error as? ServerError == .forbidden) }
        let current = try await owner.fileCollaborators(fileID: file.id)
        let writer = try #require(current.first { $0.userID == readerIdentity.userID })
        try await owner.changeFileCollaborator(file: file, original: writer, permission: nil, expectedIdentity: identity)
        await state.load()
        #expect(state.items.isEmpty && state.error == ServerError.forbidden.localizedDescription && reader.account != nil)
        let after = try await reader.listFiles(parentID: 0, recycleBin: false)
        #expect(!after.contains { $0.id == file.id })
        let denied = directory.appending(path: "Denied.txt")
        let deniedJob = try await manager.enqueueDownload(readable, destination: denied, expectedIdentity: readerIdentity)
        await manager.waitUntilIdle(deniedJob)
        #expect(manager.records[deniedJob]?.phase != .completed && !FileManager.default.fileExists(atPath: denied.path))
        let retained = try await owner.listFiles(parentID: 0, recycleBin: false)
        #expect(retained.contains { $0.id == file.id && $0.userID == identity.userID && $0.filename == "Collaborator renamed.txt" })
        await manager.close()
        state.cancel()
    }
}
