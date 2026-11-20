import Foundation
import CryptoKit
import Testing
@testable import CloudNestCore

private enum TransferTestError: Error { case timeout, failed(String) }

@MainActor private func waitForCheckpoint(_ manager: TransferManager, id: UUID) async throws {
    let deadline = ContinuousClock.now.advanced(by: .seconds(90))
    while let job = manager.records[id] {
        if job.confirmedBytes >= 4 * 1024 * 1024 && job.phase == .running { return }
        if job.phase == .failed { throw TransferTestError.failed(job.error ?? "unknown transfer failure") }
        if ContinuousClock.now >= deadline { throw TransferTestError.timeout }
        try await Task.sleep(for: .milliseconds(5))
    }
    throw TransferTestError.timeout
}

@Test(.enabled(if: ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_ORIGIN"] != nil &&
               ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_RESTRICTED"] == "0"))
@MainActor func realBackendTransfers() async throws {
    let env = ProcessInfo.processInfo.environment
    let origin = try ServerOrigin(#require(env["CLOUDNEST_SWIFT_ORIGIN"]))
    let certificateURL = URL(fileURLWithPath: try #require(env["CLOUDNEST_SWIFT_CERTIFICATE"]))
    let delegate = try FixtureTLSDelegate(der: Data(contentsOf: certificateURL))
    let configuration = URLSessionConfiguration.ephemeral
    configuration.timeoutIntervalForResource = 180
    let transport = URLSession(configuration: configuration, delegate: delegate, delegateQueue: nil)
    defer { transport.invalidateAndCancel() }
    let api = ServerAPI(session: transport, delegate: delegate)
    let vault = KeychainSessionVault(service: "org.cloudnest.macos.transfer-test.\(UUID())")
    defer { try? vault.clear() }
    let state = SessionState(api: api, vault: vault)
    await state.restore()
    await state.signIn(server: origin.url.absoluteString,
                       username: try #require(env["CLOUDNEST_SWIFT_USERNAME"]),
                       password: try #require(env["CLOUDNEST_SWIFT_PASSWORD"]))
    let root = FileManager.default.temporaryDirectory.appending(path: "cloudnest-real-transfer-\(UUID())")
    try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
    defer { try? FileManager.default.removeItem(at: root) }
    let journal = root.appending(path: "journal")
    let source = root.appending(path: "native-\(UUID()).bin")
    let payload = Data(repeating: 0x5b, count: 9 * 1024 * 1024 + 173)
    try payload.write(to: source)
    var manager = TransferManager(session: state, directory: journal)
    let upload = try await manager.enqueueUpload(source, parentID: 0)
    try await waitForCheckpoint(manager, id: upload)
    await manager.pause(upload)
    #expect(manager.records[upload]?.phase == .paused)
    #expect((manager.records[upload]?.epoch ?? 0) > 0)
    await manager.close()
    manager = TransferManager(session: state, directory: journal)
    await manager.restore()
    #expect(manager.records[upload]?.phase == .paused)
    await manager.resume(upload)
    await manager.waitUntilIdle(upload)
    #expect(manager.records[upload]?.phase == .completed)
    #expect(manager.records[upload]?.confirmedBytes == Int64(payload.count))
    let fileID = try #require(manager.records[upload]?.resultFileID)
    let files = try await state.listFiles(parentID: 0, recycleBin: false)
    let uploaded = try #require(files.first { $0.id == fileID })
    let target = root.appending(path: "downloaded.bin")
    let download = try await manager.enqueueDownload(uploaded, destination: target)
    try await waitForCheckpoint(manager, id: download)
    await manager.pause(download)
    #expect(manager.records[download]?.phase == .paused)
    let confirmed = try #require(manager.records[download]?.confirmedBytes)
    let part = root.appending(path: ".cloudnest-\(download).part")
    let partial = try FileHandle(forWritingTo: part)
    try partial.seekToEnd()
    try partial.write(contentsOf: Data("unconfirmed crash suffix".utf8))
    try partial.close()
    await manager.close()
    manager = TransferManager(session: state, directory: journal)
    await manager.restore()
    #expect(manager.records[download]?.confirmedBytes == confirmed)
    await manager.resume(download)
    await manager.waitUntilIdle(download)
    #expect(manager.records[download]?.phase == .completed)
    #expect(SHA256.hash(data: try Data(contentsOf: target)) == SHA256.hash(data: payload))
    #expect(!FileManager.default.fileExists(atPath: part.path))

    let emptyURL = root.appending(path: "empty-\(UUID()).bin")
    try Data().write(to: emptyURL)
    let empty = try await manager.enqueueUpload(emptyURL, parentID: 0)
    await manager.waitUntilIdle(empty)
    #expect(manager.records[empty]?.phase == .completed)
    #expect(manager.records[empty]?.totalBytes == 0)

    let cancelledSource = root.appending(path: "cancelled-\(UUID()).bin")
    try payload.write(to: cancelledSource)
    let cancelled = try await manager.enqueueUpload(cancelledSource, parentID: 0)
    try await waitForCheckpoint(manager, id: cancelled)
    await manager.cancel(cancelled)
    #expect(manager.records[cancelled]?.phase == .cancelled)
    let current = try await state.listFiles(parentID: 0, recycleBin: false)
    #expect(!current.contains { $0.filename == cancelledSource.lastPathComponent })
    await manager.close()
    await state.signOut()
}
