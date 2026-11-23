import Foundation
import Testing
@testable import CloudNestCore

private final class WorkspaceTestVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ value: SavedSession) throws { saved = value }
    func clear() throws { saved = nil }
}

extension RealBackendTests {
    @Test(.enabled(if: ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_WORKSPACE_TEST"] == "1"))
    @MainActor func realBackendWorkspaceRolesFilesAndRevocation() async throws {
        let env = ProcessInfo.processInfo.environment
        let origin = try ServerOrigin(#require(env["CLOUDNEST_SWIFT_ORIGIN"]))
        let certificate = try #require(env["CLOUDNEST_SWIFT_CERTIFICATE"])
        let delegate = try FixtureTLSDelegate(der: Data(contentsOf: URL(fileURLWithPath: certificate)))
        let transport = URLSession(configuration: .ephemeral, delegate: delegate, delegateQueue: nil)
        defer { transport.invalidateAndCancel() }
        let api = ServerAPI(session: transport, delegate: delegate)
        let owner = SessionState(api: api, vault: WorkspaceTestVault())
        await owner.restore()
        let password = try #require(env["CLOUDNEST_SWIFT_PASSWORD"])
        await owner.signIn(server: origin.url.absoluteString, username: try #require(env["CLOUDNEST_SWIFT_USERNAME"]), password: password)
        let ownerIdentity = try #require(owner.transferIdentity)
        let browser = WorkspaceBrowserState(session: owner)
        if env["CLOUDNEST_SWIFT_WORKSPACE_ROLE"] == "restricted" {
            await browser.load()
            #expect(browser.items.isEmpty && browser.error == ServerError.forbidden.localizedDescription)
            do {
                _ = try await owner.createWorkspace(name: "Denied workspace", expectedIdentity: ownerIdentity)
                Issue.record("Restricted account created a workspace")
            } catch { #expect(error as? ServerError == .forbidden) }
            #expect(owner.account != nil)
        } else {
            let legacyText = try #require(env["CLOUDNEST_SWIFT_WORKSPACE_LEGACY"])
            let legacyID = try #require(Int64(legacyText))
            await browser.load()
            let legacy = try #require(browser.items.first { $0.id == legacyID })
            #expect(FileBrowserRoot(workspace: legacy) == nil)
            let created = try await owner.createWorkspace(name: "Native workspace", expectedIdentity: ownerIdentity)
            #expect(created.role == .owner && created.ownerID == ownerIdentity.userID)
            let root = try #require(FileBrowserRoot(workspace: created))
            #expect(root.id > 0 && root.canWrite)
            let viewerName = try #require(env["CLOUDNEST_SWIFT_WORKSPACE_VIEWER"])
            try await owner.addWorkspaceMember(workspaceID: created.id, username: viewerName, role: .viewer, expectedIdentity: ownerIdentity)
            do {
                try await owner.addWorkspaceMember(workspaceID: created.id, username: viewerName, role: .admin, expectedIdentity: ownerIdentity)
                Issue.record("Create-only invitation overwrote an existing member")
            } catch { #expect(error as? ServerError == .conflict) }
            let viewer = SessionState(api: api, vault: WorkspaceTestVault())
            await viewer.restore()
            await viewer.signIn(server: origin.url.absoluteString, username: viewerName, password: password)
            let viewerIdentity = try #require(viewer.transferIdentity)
            let privateSpace = try await viewer.createWorkspace(name: "Other account private workspace", expectedIdentity: viewerIdentity)
            await browser.load()
            #expect(!browser.items.contains { $0.id == privateSpace.id })
            let viewerSpaces = try await viewer.workspaces()
            let readable = try #require(viewerSpaces.first { $0.id == created.id })
            let readonlyRoot = try #require(FileBrowserRoot(workspace: readable))
            #expect(!readonlyRoot.canWrite)
            try await owner.createFolder(name: "Shared folder", parentID: root.id)
            let folderRows = try await owner.listFiles(parentID: root.id, recycleBin: false)
            let folder = try #require(folderRows.first { $0.filename == "Shared folder" })
            let sourceText = try #require(env["CLOUDNEST_SWIFT_WORKSPACE_SOURCE"])
            let sourceID = try #require(Int64(sourceText))
            try await owner.relocateFiles(ids: [sourceID], parentID: root.id, copy: true)
            let files = FileBrowserState(session: viewer, recycleBin: false, root: readonlyRoot)
            await files.reload()
            #expect(files.parentID == root.id && files.items.contains { $0.id == folder.id })
            let copiedFile = try #require(files.items.first { $0.filename == "Personal workspace copy source.txt" })
            let readDirectory = FileManager.default.temporaryDirectory.appending(path: "cloudnest-viewer-download-\(UUID())")
            try FileManager.default.createDirectory(at: readDirectory, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
            defer { try? FileManager.default.removeItem(at: readDirectory) }
            let readManager = TransferManager(session: viewer, directory: readDirectory.appending(path: "journal"))
            let readTarget = readDirectory.appending(path: "Viewer.txt")
            let readJob = try await readManager.enqueueDownload(copiedFile, destination: readTarget, expectedIdentity: viewerIdentity)
            await readManager.waitUntilIdle(readJob)
            #expect(readManager.records[readJob]?.phase == .completed)
            #expect(try Data(contentsOf: readTarget) == Data("Native workspace copy source.\n".utf8))
            await readManager.close()
            await files.open(folder)
            #expect(files.parentID == folder.id)
            await files.navigate(to: 0)
            #expect(files.parentID == root.id && files.title == created.name)
            await files.createFolder(name: "Local read-only rejection")
            #expect(files.error == ServerError.forbidden.localizedDescription)
            do {
                try await viewer.createFolder(name: "Server read-only rejection", parentID: root.id)
                Issue.record("Viewer wrote to workspace")
            } catch { #expect(error as? ServerError == .forbidden) }
            let rows = try await owner.workspaceMembers(workspaceID: created.id)
            let originalViewer = try #require(rows.first { $0.userID == viewerIdentity.userID })
            do {
                try await owner.changeWorkspaceMember(workspaceID: created.id + 100000, original: originalViewer, role: .admin, expectedIdentity: ownerIdentity)
                Issue.record("Member editor submitted under another workspace selection")
            } catch { #expect(error as? ServerError == .invalidOperation) }
            try await owner.changeWorkspaceMember(workspaceID: created.id, original: originalViewer, role: .editor, expectedIdentity: ownerIdentity)
            do {
                try await owner.changeWorkspaceMember(workspaceID: created.id, original: originalViewer, role: .admin, expectedIdentity: ownerIdentity)
                Issue.record("Stale member editor overwrote a newer role")
            } catch { #expect(error as? ServerError == .conflict) }
            do {
                try await owner.changeWorkspaceMember(workspaceID: created.id, original: originalViewer, role: nil, expectedIdentity: ownerIdentity)
                Issue.record("Stale removal deleted a changed member")
            } catch { #expect(error as? ServerError == .conflict) }
            let editorSpaces = try await viewer.workspaces()
            let writable = try #require(editorSpaces.first { $0.id == created.id })
            let writeRoot = try #require(FileBrowserRoot(workspace: writable))
            let editorBrowser = FileBrowserState(session: viewer, recycleBin: false, root: writeRoot)
            await editorBrowser.reload()
            await editorBrowser.createFolder(name: "Editor created folder")
            #expect(editorBrowser.error == nil)
            let directory = FileManager.default.temporaryDirectory.appending(path: "cloudnest-workspace-transfer-\(UUID())")
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
            defer { try? FileManager.default.removeItem(at: directory) }
            let body = Data("Real native workspace upload and download.\n".utf8)
            let source = directory.appending(path: "Editor workspace upload.txt")
            try body.write(to: source)
            let manager = TransferManager(session: viewer, directory: directory.appending(path: "journal"))
            let upload = try await manager.enqueueUpload(source, parentID: root.id, expectedIdentity: viewerIdentity)
            await manager.waitUntilIdle(upload)
            #expect(manager.records[upload]?.phase == .completed)
            let fileID = try #require(manager.records[upload]?.resultFileID)
            let afterUpload = try await owner.listFiles(parentID: root.id, recycleBin: false)
            let uploaded = try #require(afterUpload.first { $0.id == fileID })
            let destination = directory.appending(path: "Downloaded.txt")
            let download = try await manager.enqueueDownload(uploaded, destination: destination, expectedIdentity: viewerIdentity)
            await manager.waitUntilIdle(download)
            #expect(manager.records[download]?.phase == .completed)
            #expect(try Data(contentsOf: destination) == body)
            await manager.close()
            do {
                try await owner.relocateFiles(ids: [fileID], parentID: 0, copy: true)
                Issue.record("Workspace file was copied out to personal scope")
            } catch { #expect(error as? ServerError == .forbidden) }
            let thirdName = try #require(env["CLOUDNEST_SWIFT_WORKSPACE_THIRD"])
            try await owner.addWorkspaceMember(workspaceID: created.id, username: thirdName, role: .viewer, expectedIdentity: ownerIdentity)
            let editRows = try await owner.workspaceMembers(workspaceID: created.id)
            let editorRow = try #require(editRows.first { $0.userID == viewerIdentity.userID })
            let thirdRow = try #require(editRows.first { $0.username == thirdName })
            try await owner.changeWorkspaceMember(workspaceID: created.id, original: editorRow, role: .admin, expectedIdentity: ownerIdentity)
            try await viewer.changeWorkspaceMember(workspaceID: created.id, original: thirdRow, role: .editor, expectedIdentity: viewerIdentity)
            let adminRows = try await owner.workspaceMembers(workspaceID: created.id)
            let thirdEditor = try #require(adminRows.first { $0.userID == thirdRow.userID })
            do {
                try await viewer.changeWorkspaceMember(workspaceID: created.id, original: thirdEditor, role: .admin, expectedIdentity: viewerIdentity)
                Issue.record("Admin promoted another admin")
            } catch { #expect(error as? ServerError == .forbidden) }
            try await owner.changeWorkspaceMember(workspaceID: created.id, original: thirdEditor, role: .admin, expectedIdentity: ownerIdentity)
            let finalRows = try await owner.workspaceMembers(workspaceID: created.id)
            let thirdAdmin = try #require(finalRows.first { $0.userID == thirdRow.userID })
            do {
                try await viewer.changeWorkspaceMember(workspaceID: created.id, original: thirdAdmin, role: nil, expectedIdentity: viewerIdentity)
                Issue.record("Admin removed another admin")
            } catch { #expect(error as? ServerError == .forbidden) }
            let viewerAdmin = try #require(finalRows.first { $0.userID == viewerIdentity.userID })
            try await owner.changeWorkspaceMember(workspaceID: created.id, original: viewerAdmin, role: nil, expectedIdentity: ownerIdentity)
            let revokedSpaces = try await viewer.workspaces()
            #expect(!revokedSpaces.contains { $0.id == created.id })
            await editorBrowser.reload()
            #expect(editorBrowser.items.isEmpty && editorBrowser.error == ServerError.forbidden.localizedDescription)
            do {
                _ = try await viewer.fileVersions(fileID: fileID)
                Issue.record("Removed member still read its workspace upload")
            } catch { #expect(error as? ServerError == .forbidden) }
            let retained = try await owner.listFiles(parentID: root.id, recycleBin: false)
            #expect(retained.contains { $0.id == fileID })
            #expect(viewer.account != nil)
            await viewer.signOut()
        }
        browser.cancel()
        await owner.signOut()
    }
}
