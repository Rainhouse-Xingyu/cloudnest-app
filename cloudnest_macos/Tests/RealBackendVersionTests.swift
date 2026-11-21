import Foundation
import Testing
@testable import CloudNestCore

private final class VersionTestVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ session: SavedSession) throws { saved = session }
    func clear() throws { saved = nil }
}

private enum VersionTestError: Error { case checkpointTimeout }

extension RealBackendTests {
@Test(.enabled(if: ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_ORIGIN"] != nil &&
               ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_RESTRICTED"] == "0"))
@MainActor func realBackendVersionHistory() async throws {
    let env = ProcessInfo.processInfo.environment
    let origin = try ServerOrigin(#require(env["CLOUDNEST_SWIFT_ORIGIN"]))
    let delegate = try FixtureTLSDelegate(der: Data(contentsOf: URL(fileURLWithPath: #require(env["CLOUDNEST_SWIFT_CERTIFICATE"]))))
    let transport = URLSession(configuration: .ephemeral, delegate: delegate, delegateQueue: nil)
    defer { transport.invalidateAndCancel() }
    let api = ServerAPI(session: transport, delegate: delegate)
    let state = SessionState(api: api, vault: VersionTestVault())
    await state.restore()
    let username = try #require(env["CLOUDNEST_SWIFT_USERNAME"])
    let password = try #require(env["CLOUDNEST_SWIFT_PASSWORD"])
    await state.signIn(server: origin.url.absoluteString, username: username, password: password)
    let versionFile = try #require(env["CLOUDNEST_SWIFT_VERSION_FILE"])
    let fileID = try #require(Int64(versionFile))
    let files = try await state.listFiles(parentID: 0, recycleBin: false)
    let file = try #require(files.first { $0.id == fileID })
    let history = FileVersionState(file: file, session: state)
    await history.reload()
    #expect(history.error == nil)
    #expect(history.currentVersion == 2)
    #expect(history.items.map(\.version) == [2, 1])
    let original = try #require(history.items.first { $0.version == 1 })
    let payload = Data(repeating: 0x31, count: 9 * 1024 * 1024 + 173)
    #expect(original.size == Int64(payload.count))
    #expect(original.created != nil)
    let root = FileManager.default.temporaryDirectory.appending(path: "cloudnest-native-version-\(UUID())")
    try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
    defer { try? FileManager.default.removeItem(at: root) }
    let journal = root.appending(path: "journal")
    var manager = TransferManager(session: state, directory: journal)
    let target = root.appending(path: "version-one.bin")
    let task = try await manager.enqueueVersionDownload(file, version: original, destination: target)
    let deadline = ContinuousClock.now.advanced(by: .seconds(90))
    while manager.records[task]?.phase == .running && (manager.records[task]?.confirmedBytes ?? 0) < 4 * 1024 * 1024 {
        guard ContinuousClock.now < deadline else { throw VersionTestError.checkpointTimeout }
        try await Task.sleep(for: .milliseconds(5))
    }
    #expect(manager.records[task]?.phase == .running)
    await manager.pause(task)
    #expect(manager.records[task]?.phase == .paused)
    let confirmed = try #require(manager.records[task]?.confirmedBytes)
    #expect(confirmed >= 4 * 1024 * 1024)
    await manager.close()

    // Another window advances the current file while this historical download
    // is paused. Its persisted selector must continue to request version one.
    let context = try state.transferContext()
    let advanced = try await api.restoreVersion(origin, token: context.token, fileID: fileID, version: 2, baseVersion: 2)
    #expect(advanced.version == 3)
    manager = TransferManager(session: state, directory: journal)
    await manager.restore()
    #expect(manager.records[task]?.fileVersion == 1)
    #expect(manager.records[task]?.versionSize == Int64(payload.count))
    #expect(manager.records[task]?.confirmedBytes == confirmed)
    await manager.resume(task)
    await manager.waitUntilIdle(task)
    #expect(manager.records[task]?.phase == .completed)
    let historicalURL = try await manager.resultURL(task)
    #expect(try Data(contentsOf: historicalURL) == payload)
    let currentTask = try await manager.enqueueDownload(file, destination: root.appending(path: "current.bin"))
    await manager.waitUntilIdle(currentTask)
    #expect(manager.records[currentTask]?.phase == .completed)
    let currentURL = try await manager.resultURL(currentTask)
    #expect(try Data(contentsOf: currentURL) == Data("Current version for native history\n".utf8))

    // The sheet confirmed base two. A stale confirmation must not restore over
    // the unseen version three or silently retry with its new base.
    let stale = await history.restore(original, expectedCurrentVersion: 2)
    #expect(!stale)
    #expect(history.error != nil)
    #expect(history.items.isEmpty)
    #expect(try await state.fileVersions(fileID: fileID).currentVersion == 3)
    await history.reload()
    #expect(history.currentVersion == 3)
    let restored = await history.restore(original, expectedCurrentVersion: 3)
    #expect(restored)
    #expect(history.error == nil)
    #expect(history.currentVersion == 4)
    #expect(history.items.map(\.version) == [4, 3, 2, 1])
    let updated = try #require(try await state.listFiles(parentID: 0, recycleBin: false).first { $0.id == fileID })
    let restoredTask = try await manager.enqueueDownload(updated, destination: root.appending(path: "restored.bin"))
    await manager.waitUntilIdle(restoredTask)
    #expect(manager.records[restoredTask]?.phase == .completed)
    let restoredURL = try await manager.resultURL(restoredTask)
    #expect(try Data(contentsOf: restoredURL) == payload)
    let forbiddenValue = try #require(env["CLOUDNEST_SWIFT_FORBIDDEN_FILE"])
    let forbidden = try #require(Int64(forbiddenValue))
    do {
        _ = try await state.fileVersions(fileID: forbidden)
        Issue.record("Another user's history was visible")
    } catch { #expect(error as? ServerError == .forbidden) }

    // More than one native page, via real append operations. Navigation keeps
    // only one page and must refuse to combine pages from different heads.
    for base in 4..<56 {
        _ = try await api.restoreVersion(origin, token: context.token, fileID: fileID, version: 2, baseVersion: base)
    }
    await history.reload()
    #expect(history.currentVersion == 56)
    #expect(history.items.map(\.version) == Array((7...56).reversed()))
    #expect(history.page?.hasOlder == true)
    #expect(history.page?.hasNewer == false)
    await history.loadPage(older: true)
    #expect(history.items.map(\.version) == [6, 5, 4, 3, 2, 1])
    #expect(history.page?.hasOlder == false)
    #expect(history.page?.hasNewer == true)
    await history.loadPage(older: false)
    #expect(history.items.map(\.version) == Array((7...56).reversed()))
    _ = try await api.restoreVersion(origin, token: context.token, fileID: fileID, version: 2, baseVersion: 56)
    await history.loadPage(older: true)
    #expect(history.items.isEmpty)
    #expect(history.currentVersion == nil)
    #expect(history.error != nil)
    await history.reload()
    #expect(history.currentVersion == 57)
    #expect(history.items.count == 50)
    await manager.close()
    await state.signOut()
    await history.reload()
    #expect(history.items.isEmpty)
    #expect(history.currentVersion == nil)
}
}
