import Foundation
import Testing
@testable import CloudNestCore

private final class VersionUploadTestVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ session: SavedSession) throws { saved = session }
    func clear() throws { saved = nil }
}

private enum VersionUploadTestError: Error { case timeout, failed(String) }

extension RealBackendTests {
@Test(.enabled(if: ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_ORIGIN"] != nil &&
               ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_RESTRICTED"] == "0"))
@MainActor func realBackendVersionUpload() async throws {
    let env = ProcessInfo.processInfo.environment
    let origin = try ServerOrigin(#require(env["CLOUDNEST_SWIFT_ORIGIN"]))
    let delegate = try FixtureTLSDelegate(der: Data(contentsOf: URL(fileURLWithPath: #require(env["CLOUDNEST_SWIFT_CERTIFICATE"]))))
    let transport = URLSession(configuration: .ephemeral, delegate: delegate, delegateQueue: nil)
    defer { transport.invalidateAndCancel() }
    let api = ServerAPI(session: transport, delegate: delegate)
    let state = SessionState(api: api, vault: VersionUploadTestVault())
    await state.restore()
    let username = try #require(env["CLOUDNEST_SWIFT_USERNAME"])
    let password = try #require(env["CLOUDNEST_SWIFT_PASSWORD"])
    await state.signIn(server: origin.url.absoluteString, username: username, password: password)
    let context = try state.transferContext()
    let root = FileManager.default.temporaryDirectory.appending(path: "cloudnest-native-version-upload-\(UUID())")
    try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
    defer { try? FileManager.default.removeItem(at: root) }
    let journal = root.appending(path: "journal")
    var manager = TransferManager(session: state, directory: journal)
    let original = root.appending(path: "target-\(UUID()).bin")
    let originalBytes = Data("Original native version upload\n".utf8)
    try originalBytes.write(to: original)
    let first = try await manager.enqueueUpload(original, parentID: 0)
    await manager.waitUntilIdle(first)
    #expect(manager.records[first]?.phase == .completed)
    let fileID = try #require(manager.records[first]?.resultFileID)
    let files = try await state.listFiles(parentID: 0, recycleBin: false)
    let file = try #require(files.first { $0.id == fileID })
    let history = FileVersionState(file: file, session: state)
    await history.reload()
    #expect(history.currentVersion == 1)
    let source = root.appending(path: "different-local-source.bin")
    let payload = Data(repeating: 0x6f, count: 9 * 1024 * 1024 + 173)
    try payload.write(to: source)
    let pending = try #require(await history.upload(source, expectedCurrentVersion: 1, comment: "Confirmed base one", transfers: manager))
    let deadline = ContinuousClock.now.advanced(by: .seconds(90))
    while (manager.records[pending]?.confirmedBytes ?? 0) < 4 * 1024 * 1024 {
        if let job = manager.records[pending], job.phase == .failed { throw VersionUploadTestError.failed(job.error ?? "upload failed") }
        guard ContinuousClock.now < deadline else { throw VersionUploadTestError.timeout }
        try await Task.sleep(for: .milliseconds(5))
    }
    await manager.pause(pending)
    #expect(manager.records[pending]?.phase == .paused)
    await manager.close()
    _ = try await api.restoreVersion(origin, token: context.token, fileID: fileID, version: 1, baseVersion: 1)
    manager = TransferManager(session: state, directory: journal)
    await manager.restore()
    #expect(manager.records[pending]?.direction == .versionUpload)
    #expect(manager.records[pending]?.fileID == fileID)
    #expect(manager.records[pending]?.baseVersion == 1)
    await manager.resume(pending)
    await manager.waitUntilIdle(pending)
    #expect(manager.records[pending]?.phase == .failed)
    #expect(manager.records[pending]?.baseVersion == 1)
    #expect(manager.records[pending]?.error?.contains("取消此任务") == true)
    await manager.cancel(pending)
    #expect(manager.records[pending]?.phase == .cancelled)
    await history.reload()
    #expect(history.currentVersion == 2)
    let replacement = try #require(await history.upload(source, expectedCurrentVersion: 2, comment: "Native version replacement", transfers: manager))
    await manager.waitUntilIdle(replacement)
    #expect(manager.records[replacement]?.phase == .completed)
    #expect(manager.records[replacement]?.publishedVersion == 3)
    #expect(manager.records[replacement]?.resultFileID == fileID)
    #expect(await history.observeUpload(manager.records[replacement]))
    #expect(history.currentVersion == 3)
    #expect(history.items.map(\.version) == [3, 2, 1])
    let latest = try await state.listFiles(parentID: 0, recycleBin: false)
    let updated = try #require(latest.first { $0.id == fileID })
    #expect(updated.filename == file.filename)
    #expect(updated.size == Int64(payload.count))
    #expect(!latest.contains { $0.filename == source.lastPathComponent })
    let download = try await manager.enqueueDownload(updated, destination: root.appending(path: "current.bin"))
    await manager.waitUntilIdle(download)
    #expect(manager.records[download]?.phase == .completed)
    let downloaded = try await manager.resultURL(download)
    #expect(try Data(contentsOf: downloaded) == payload)
    let oldVersion = try #require(history.items.first { $0.version == 1 })
    let old = try await manager.enqueueVersionDownload(updated, version: oldVersion, destination: root.appending(path: "original.bin"))
    await manager.waitUntilIdle(old)
    let oldURL = try await manager.resultURL(old)
    #expect(try Data(contentsOf: oldURL) == originalBytes)
    // An initialization rejected before allocation must still be cancellable by
    // its original request key; cancellation must not rebase and create work.
    let stale = try await manager.enqueueVersionUpload(source, file: file, baseVersion: 1, comment: "Stale before initialization")
    await manager.waitUntilIdle(stale)
    #expect(manager.records[stale]?.phase == .failed)
    #expect(manager.records[stale]?.uploadID == nil)
    await manager.cancel(stale)
    #expect(manager.records[stale]?.phase == .cancelled)
    #expect(try await state.fileVersions(fileID: fileID).currentVersion == 3)
    await manager.close()

    // The test router really allocates the correct ticket, then corrupts only
    // its binding response. No upload body may be sent under that response.
    let faultyConfiguration = URLSessionConfiguration.ephemeral
    faultyConfiguration.httpAdditionalHeaders = ["X-CloudNest-Test-Version-Binding": "native-version-binding-\(UUID())"]
    let faultyTransport = URLSession(configuration: faultyConfiguration, delegate: delegate, delegateQueue: nil)
    defer { faultyTransport.invalidateAndCancel() }
    let faultyAPI = ServerAPI(session: faultyTransport, delegate: delegate)
    let faultyState = SessionState(api: faultyAPI, vault: VersionUploadTestVault())
    await faultyState.restore()
    await faultyState.signIn(server: origin.url.absoluteString, username: username, password: password)
    let faulty = TransferManager(session: faultyState, directory: root.appending(path: "faulty-journal"))
    let bad = try await faulty.enqueueVersionUpload(source, file: updated, baseVersion: 3, comment: "Invalid binding response")
    await faulty.waitUntilIdle(bad)
    #expect(faulty.records[bad]?.phase == .failed)
    #expect(faulty.records[bad]?.confirmedBytes == 0)
    #expect(faulty.records[bad]?.uploadID == nil)
    await faulty.cancel(bad)
    #expect(faulty.records[bad]?.phase == .cancelled)
    #expect(try await state.fileVersions(fileID: fileID).currentVersion == 3)
    await faulty.close()
    await faultyState.signOut()
    await state.signOut()
}
}
