import Foundation
import Testing
@testable import CloudNestCore

@Test(.enabled(if: ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_ORIGIN"] != nil &&
               ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_RESTRICTED"] == "0"))
@MainActor func realBackendInitializationResponseLoss() async throws {
    let env = ProcessInfo.processInfo.environment
    let origin = try ServerOrigin(#require(env["CLOUDNEST_SWIFT_ORIGIN"]))
    let certificateURL = URL(fileURLWithPath: try #require(env["CLOUDNEST_SWIFT_CERTIFICATE"]))
    let delegate = try FixtureTLSDelegate(der: Data(contentsOf: certificateURL))
    let transport = URLSession(configuration: .ephemeral, delegate: delegate, delegateQueue: nil)
    defer { transport.invalidateAndCancel() }
    let api = ServerAPI(session: transport, delegate: delegate)
    let vault = KeychainSessionVault(service: "org.cloudnest.macos.init-test.\(UUID())")
    defer { try? vault.clear() }
    let state = SessionState(api: api, vault: vault)
    await state.restore()
    await state.signIn(server: origin.url.absoluteString,
                       username: try #require(env["CLOUDNEST_SWIFT_USERNAME"]),
                       password: try #require(env["CLOUDNEST_SWIFT_PASSWORD"]))
    let root = FileManager.default.temporaryDirectory.appending(path: "cloudnest-lost-init-\(UUID())")
    try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
    defer { try? FileManager.default.removeItem(at: root) }

    for action in ["resume", "pause", "cancel"] {
        let source = root.appending(path: "lost-init-\(action)-\(UUID()).bin")
        try Data(repeating: 0x39, count: 1024).write(to: source)
        let journal = root.appending(path: action)
        var manager = TransferManager(session: state, directory: journal)
        let id = try await manager.enqueueUpload(source, parentID: 0)
        await manager.waitUntilIdle(id)
        // The real Go router commits, then closes TCP before writing the reply.
        #expect(manager.records[id]?.phase == .failed)
        #expect(manager.records[id]?.source != nil)
        #expect(manager.records[id]?.uploadID == nil)
        await manager.close()
        manager = TransferManager(session: state, directory: journal)
        await manager.restore()
        let context = try state.transferContext()
        if action == "cancel" {
            await manager.cancel(id)
            #expect(manager.records[id]?.phase == .cancelled)
            let uploadID = try #require(manager.records[id]?.uploadID)
            let status = try await api.uploadProgress(context, id: uploadID)
            #expect(status.status == 3)
            let files = try await state.listFiles(parentID: 0, recycleBin: false)
            #expect(!files.contains { $0.filename == source.lastPathComponent })
        } else {
            if action == "pause" {
                await manager.pause(id)
                #expect(manager.records[id]?.phase == .paused)
                let uploadID = try #require(manager.records[id]?.uploadID)
                let paused = try await api.uploadProgress(context, id: uploadID)
                #expect(paused.status == 2)
                #expect(paused.epoch > 0)
            }
            await manager.resume(id)
            await manager.waitUntilIdle(id)
            #expect(manager.records[id]?.phase == .completed)
            #expect(manager.records[id]?.confirmedBytes == 1024)
            #expect(manager.records[id]?.resultFileID != nil)
            let files = try await state.listFiles(parentID: 0, recycleBin: false)
            #expect(files.filter { $0.filename == source.lastPathComponent }.count == 1)
        }
        let originalID = try #require(manager.records[id]?.uploadID)
        let manifest = try #require(manager.records[id]?.source)
        let replay = try await api.initializeUpload(context, requestID: id, name: source.lastPathComponent,
                                                    parentID: 0, source: manifest)
        #expect(replay.uploadID == originalID)
        #expect(replay.status == (action == "cancel" ? 3 : 1))
        await manager.close()
    }
    await state.signOut()
}
