import Foundation
import Testing
@testable import CloudNestCore

private final class RealIntentVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ session: SavedSession) throws { saved = session }
    func clear() throws { saved = nil }
}

extension RealBackendTests {
    @Test(.enabled(if: ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_ORIGIN"] != nil &&
                   ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_RESTRICTED"] == "0"))
    @MainActor func realBackendTransferIntentStaysWithItsAccount() async throws {
        let env = ProcessInfo.processInfo.environment
        let origin = try ServerOrigin(#require(env["CLOUDNEST_SWIFT_ORIGIN"]))
        let delegate = try FixtureTLSDelegate(der: Data(contentsOf: URL(fileURLWithPath: #require(env["CLOUDNEST_SWIFT_CERTIFICATE"]))))
        let transport = URLSession(configuration: .ephemeral, delegate: delegate, delegateQueue: nil)
        defer { transport.invalidateAndCancel() }
        let state = SessionState(api: ServerAPI(session: transport, delegate: delegate), vault: RealIntentVault())
        await state.restore()
        let password = try #require(env["CLOUDNEST_SWIFT_PASSWORD"])
        await state.signIn(server: origin.url.absoluteString, username: try #require(env["CLOUDNEST_SWIFT_USERNAME"]), password: password)
        let original = try #require(state.transferIdentity)
        let browser = FileBrowserState(session: state, recycleBin: false)
        await browser.reload()
        let originalFile = try #require(browser.items.first { !$0.isDirectory })
        let history = try await state.fileVersions(fileID: originalFile.id)
        let version = try #require(history.items.first)
        let root = FileManager.default.temporaryDirectory.appending(path: "cloudnest-real-intent-\(UUID())")
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let source = root.appending(path: "Intent-\(UUID()).txt")
        let payload = Data("Only a fresh account selection may upload these bytes.\n".utf8)
        try payload.write(to: source)
        let manager = TransferManager(session: state, directory: root.appending(path: "journal"))
        await manager.restore()
        await state.signOut()
        await state.signIn(server: origin.url.absoluteString, username: try #require(env["CLOUDNEST_SWIFT_OTHER_USERNAME"]), password: password)
        let current = try #require(state.transferIdentity)
        #expect(original.server == current.server)
        #expect(original.userID != current.userID)
        let before = try await state.listFiles(parentID: 0, recycleBin: false)
        for operation in 0..<5 {
            do {
                switch operation {
                case 0: _ = try await manager.enqueueUpload(source, parentID: 0, expectedIdentity: original)
                case 1: _ = try await manager.enqueueDownload(originalFile, destination: root.appending(path: "stale-current.txt"), expectedIdentity: original)
                case 2: _ = try await manager.enqueueVersionUpload(source, file: originalFile, baseVersion: history.currentVersion, comment: "", expectedIdentity: original)
                case 3: _ = try await manager.enqueueVersionDownload(originalFile, version: version, destination: root.appending(path: "stale-version.txt"), expectedIdentity: original)
                default: _ = try await manager.enqueuePreview(originalFile, expectedIdentity: original)
                }
                Issue.record("Old account's selection reached the new account")
            } catch { #expect(error as? ServerError == .sessionChanged) }
        }
        #expect(manager.records.isEmpty)
        await browser.createFolder(name: "Stale-\(UUID())")
        #expect(browser.error == ServerError.sessionChanged.localizedDescription)
        await browser.reload()
        #expect(browser.items.isEmpty)
        #expect(try await state.listFiles(parentID: 0, recycleBin: false) == before)

        // A new, explicit selection still works with the new account.
        let uploaded = try await manager.enqueueUpload(source, parentID: 0, expectedIdentity: current)
        await manager.waitUntilIdle(uploaded)
        #expect(manager.records[uploaded]?.phase == .completed)
        #expect(manager.records[uploaded]?.identity == current)
        let fileID = try #require(manager.records[uploaded]?.resultFileID)
        let file = try #require(try await state.listFiles(parentID: 0, recycleBin: false).first { $0.id == fileID })
        let downloaded = try await manager.enqueueDownload(file, destination: root.appending(path: "fresh.txt"), expectedIdentity: current)
        await manager.waitUntilIdle(downloaded)
        #expect(manager.records[downloaded]?.phase == .completed)
        let output = try await manager.resultURL(downloaded)
        #expect(try Data(contentsOf: output) == payload)
        await manager.close()
        await state.signOut()
    }
}
