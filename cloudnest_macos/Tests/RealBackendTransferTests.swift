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

extension RealBackendTests {
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

    let preview = try await manager.enqueuePreview(uploaded)
    await manager.waitUntilIdle(preview)
    #expect(manager.records[preview]?.phase == .completed)
    let previewURL = try await manager.resultURL(preview)
    #expect(SHA256.hash(data: try Data(contentsOf: previewURL)) == SHA256.hash(data: payload))
    #expect(!manager.jobs.contains { $0.id == preview })
    #expect(!FileManager.default.fileExists(atPath: journal.appending(path: "\(preview).json").path))
    await manager.dismissPreview(preview)
    #expect(manager.records[preview] == nil)
    #expect(!FileManager.default.fileExists(atPath: previewURL.path))
    let folderName = "Archive-\(UUID())"
    try await state.createFolder(name: folderName, parentID: 0)
    let withFolder = try await state.listFiles(parentID: 0, recycleBin: false)
    let folder = try #require(withFolder.first { $0.filename == folderName })
    let archive = try await manager.enqueueDownload(folder, destination: root.appending(path: "folder.zip"))
    await manager.waitUntilIdle(archive)
    #expect(manager.records[archive]?.phase == .completed)
    let archiveURL = try await manager.resultURL(archive)
    let magic = try Data(contentsOf: archiveURL).prefix(2)
    #expect(magic == Data("PK".utf8))

    let emptyURL = root.appending(path: "empty-\(UUID()).bin")
    try Data().write(to: emptyURL)
    let empty = try await manager.enqueueUpload(emptyURL, parentID: 0)
    await manager.waitUntilIdle(empty)
    #expect(manager.records[empty]?.phase == .completed)
    #expect(manager.records[empty]?.totalBytes == 0)
    let emptyID = try #require(manager.records[empty]?.resultFileID)
    let refreshed = try await state.listFiles(parentID: 0, recycleBin: false)
    let emptyFile = try #require(refreshed.first { $0.id == emptyID })
    let emptyDestination = root.appending(path: "empty-download.bin")
    let emptyDownload = try await manager.enqueueDownload(emptyFile, destination: emptyDestination)
    await manager.waitUntilIdle(emptyDownload)
    #expect(manager.records[emptyDownload]?.phase == .completed)
    #expect(try Data(contentsOf: emptyDestination).isEmpty)

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

@Test(.enabled(if: ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_ORIGIN"] != nil &&
               ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_RESTRICTED"] == "0"))
@MainActor func realBackendPreviewBudgetAndDismissal() async throws {
    let env = ProcessInfo.processInfo.environment
    let origin = try ServerOrigin(#require(env["CLOUDNEST_SWIFT_ORIGIN"]))
    let certificateURL = URL(fileURLWithPath: try #require(env["CLOUDNEST_SWIFT_CERTIFICATE"]))
    let delegate = try FixtureTLSDelegate(der: Data(contentsOf: certificateURL))
    let transport = URLSession(configuration: .ephemeral, delegate: delegate, delegateQueue: nil)
    defer { transport.invalidateAndCancel() }
    let api = ServerAPI(session: transport, delegate: delegate)
    let vault = KeychainSessionVault(service: "org.cloudnest.macos.preview-test.\(UUID())")
    defer { try? vault.clear() }
    let state = SessionState(api: api, vault: vault)
    await state.restore()
    await state.signIn(server: origin.url.absoluteString,
                       username: try #require(env["CLOUDNEST_SWIFT_USERNAME"]),
                       password: try #require(env["CLOUDNEST_SWIFT_PASSWORD"]))
    let root = FileManager.default.temporaryDirectory.appending(path: "cloudnest-real-preview-\(UUID())")
    try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
    defer { try? FileManager.default.removeItem(at: root) }
    let journal = root.appending(path: "journal")
    let manager = TransferManager(session: state, directory: journal,
                                  previewLimits: .init(bytes: 4096, fileBytes: 4096, entries: 2))
    let source = root.appending(path: "preview-\(UUID()).bin")
    try Data(repeating: 0x45, count: 2048).write(to: source)
    let upload = try await manager.enqueueUpload(source, parentID: 0)
    await manager.waitUntilIdle(upload)
    #expect(manager.records[upload]?.phase == .completed)
    let fileID = try #require(manager.records[upload]?.resultFileID)
    let files = try await state.listFiles(parentID: 0, recycleBin: false)
    let file = try #require(files.first { $0.id == fileID })
    let first = try await manager.enqueuePreview(file)
    let second = try await manager.enqueuePreview(file)
    await manager.waitUntilIdle(first)
    await manager.waitUntilIdle(second)
    let firstURL = try await manager.resultURL(first)
    let secondURL = try await manager.resultURL(second)
    #expect(firstURL != secondURL)
    await #expect(throws: TransferError.previewCapacity) { try await manager.enqueuePreview(file) }
    #expect(FileManager.default.fileExists(atPath: firstURL.path))
    #expect(FileManager.default.fileExists(atPath: secondURL.path))
    await manager.dismissPreview(first)
    #expect(!FileManager.default.fileExists(atPath: firstURL.path))
    #expect(FileManager.default.fileExists(atPath: secondURL.path))
    let pending = try await manager.enqueuePreview(file)
    await manager.dismissPreview(pending)
    #expect(manager.records[pending] == nil)
    // A stale row says one byte, but the real HTTP representation is larger.
    let stale = FileItem(id: file.id, filename: file.filename, parentID: file.parentID,
                         isFolder: 0, size: 1, currentVersion: file.currentVersion,
                         updateTime: file.updateTime, fileType: file.fileType)
    let changed = try await manager.enqueuePreview(stale)
    await manager.waitUntilIdle(changed)
    #expect(manager.records[changed]?.phase == .failed)
    #expect(manager.records[changed]?.confirmedBytes == 0)
    await manager.dismissPreview(changed)
    await manager.close()
    #expect(!FileManager.default.fileExists(atPath: secondURL.path))
    let restored = TransferManager(session: state, directory: journal)
    await restored.restore()
    #expect(restored.records[upload]?.phase == .completed)
    #expect(!restored.records.values.contains { $0.previewID != nil })
    await restored.close()
    await state.signOut()
}

}
