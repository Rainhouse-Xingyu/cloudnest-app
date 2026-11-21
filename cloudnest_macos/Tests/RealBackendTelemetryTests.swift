import Foundation
import CryptoKit
import Testing
@testable import CloudNestCore

private final class TelemetryTestVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ session: SavedSession) throws { saved = session }
    func clear() throws { saved = nil }
}

private enum TelemetryTestError: Error { case timeout, failed(String) }

@MainActor private func waitForBody(_ manager: TransferManager, _ id: UUID, minimum: Int64) async throws -> TransferTelemetry.Snapshot {
    let deadline = ContinuousClock.now.advanced(by: .seconds(90))
    while ContinuousClock.now < deadline {
        if let snapshot = manager.activity(id), snapshot.bodyBytes >= minimum { return snapshot }
        if let job = manager.records[id], job.phase == .failed {
            throw TelemetryTestError.failed(job.error ?? "unknown transfer failure")
        }
        try await Task.sleep(for: .milliseconds(5))
    }
    throw TelemetryTestError.timeout
}

extension RealBackendTests {
@Test(.enabled(if: ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_ORIGIN"] != nil &&
               ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_RESTRICTED"] == "0"))
@MainActor func realBackendTransferTelemetry() async throws {
    let env = ProcessInfo.processInfo.environment
    let origin = try ServerOrigin(#require(env["CLOUDNEST_SWIFT_ORIGIN"]))
    let delegate = try FixtureTLSDelegate(der: Data(contentsOf: URL(fileURLWithPath: #require(env["CLOUDNEST_SWIFT_CERTIFICATE"]))))
    let configuration = URLSessionConfiguration.ephemeral
    configuration.timeoutIntervalForResource = 180
    configuration.httpAdditionalHeaders = ["X-CloudNest-Test-Telemetry": "native-rate-\(UUID())"]
    let transport = URLSession(configuration: configuration, delegate: delegate, delegateQueue: nil)
    defer { transport.invalidateAndCancel() }
    let api = ServerAPI(session: transport, delegate: delegate)
    let state = SessionState(api: api, vault: TelemetryTestVault())
    await state.restore()
    await state.signIn(server: origin.url.absoluteString,
                       username: try #require(env["CLOUDNEST_SWIFT_USERNAME"]),
                       password: try #require(env["CLOUDNEST_SWIFT_PASSWORD"]))
    let context = try state.transferContext()
    struct Profile: Decodable { let user: Account }
    func release(_ gate: String) async throws {
        let _: Profile = try await api.request(origin, "api/auth/me", token: context.token,
            query: [URLQueryItem(name: "test_transfer_release", value: gate)])
    }
    let root = FileManager.default.temporaryDirectory.appending(path: "cloudnest-real-rate-\(UUID())")
    try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
    defer { try? FileManager.default.removeItem(at: root) }
    let journal = root.appending(path: "journal")
    let source = root.appending(path: "telemetry-\(UUID()).bin")
    let blockBytes: Int64 = 4 * 1024 * 1024
    let payload = Data(repeating: 0x6e, count: 9 * 1024 * 1024 + 173)
    try payload.write(to: source)
    var manager = TransferManager(session: state, directory: journal)
    let upload = try await manager.enqueueUpload(source, parentID: 0)
    _ = try await waitForBody(manager, upload, minimum: 4 * 1024 * 1024)
    // The real request body has flowed while its first receipt is withheld.
    #expect(manager.records[upload]?.confirmedBytes == 0)
    try await release("upload-0")
    _ = try await waitForBody(manager, upload, minimum: 8 * 1024 * 1024)
    #expect(manager.records[upload]?.confirmedBytes == blockBytes)
    let uploadID = try #require(manager.records[upload]?.uploadID)
    let deadline = ContinuousClock.now.advanced(by: .seconds(30))
    while !(try await api.uploadProgress(context, id: uploadID).uploaded ?? []).contains(1) {
        guard ContinuousClock.now < deadline else { throw TelemetryTestError.timeout }
        try await Task.sleep(for: .milliseconds(50))
    }
    await manager.pause(upload)
    #expect(manager.records[upload]?.phase == .paused)
    #expect(manager.activity(upload) == nil)
    await manager.close()
    manager = TransferManager(session: state, directory: journal)
    await manager.restore()
    await manager.resume(upload)
    let resumedUpload = try await waitForBody(manager, upload, minimum: 1024 * 1024)
    // Server reconciliation acknowledges two old blocks but must not count
    // those eight MiB as bytes sent by this new run.
    let resumedUploadConfirmed = manager.records[upload]?.confirmedBytes
    #expect(resumedUploadConfirmed == blockBytes * 2)
    #expect(resumedUpload.bodyBytes < 2 * 1024 * 1024)
    try await release("upload-2")
    await manager.waitUntilIdle(upload)
    #expect(manager.records[upload]?.phase == .completed)
    #expect(manager.activity(upload) == nil)
    let fileID = try #require(manager.records[upload]?.resultFileID)
    let files = try await state.listFiles(parentID: 0, recycleBin: false)
    let file = try #require(files.first { $0.id == fileID })
    let target = root.appending(path: "downloaded.bin")
    let download = try await manager.enqueueDownload(file, destination: target)
    _ = try await waitForBody(manager, download, minimum: 4 * 1024 * 1024 + 1)
    #expect(manager.records[download]?.confirmedBytes == blockBytes)
    await manager.pause(download)
    #expect(manager.activity(download) == nil)
    await manager.close()
    manager = TransferManager(session: state, directory: journal)
    await manager.restore()
    await manager.resume(download)
    let resumedDownload = try await waitForBody(manager, download, minimum: 1)
    #expect(manager.records[download]?.confirmedBytes == blockBytes)
    #expect(resumedDownload.bodyBytes <= 32 * 1024)
    try await release("download")
    await manager.waitUntilIdle(download)
    #expect(manager.records[download]?.phase == .completed)
    #expect(manager.activity(download) == nil)
    let destination = try await manager.resultURL(download)
    #expect(SHA256.hash(data: try Data(contentsOf: destination)) == SHA256.hash(data: payload))
    await manager.close()
    // Exercise the new observing delegate on an actual credential-bearing
    // redirect. The fixture reports an error if any redirected route is hit.
    let redirect = try ServerOrigin(#require(env["CLOUDNEST_SWIFT_REDIRECT_ORIGIN"]))
    let redirectMeter = TransferTelemetry()
    redirectMeter.transition(to: .transferring)
    do {
        let _: LoginResponse = try await api.request(redirect, "api/auth/login",
            body: Data(#"{"username":"fixture","password":"disposable-fixture-password"}"#.utf8),
            method: "POST", transfer: true, telemetry: redirectMeter)
        Issue.record("The observing delegate accepted a redirect")
    } catch { #expect(error as? ServerError == .invalidResponse) }
    await state.signOut()
}
}
