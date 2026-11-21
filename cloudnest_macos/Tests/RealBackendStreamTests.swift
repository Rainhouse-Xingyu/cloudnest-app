import Foundation
import Testing
@testable import CloudNestCore

private final class StreamTestVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ session: SavedSession) throws { saved = session }
    func clear() throws { saved = nil }
}

extension RealBackendTests {
    @Test(.enabled(if: ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_ORIGIN"] != nil &&
                   ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_RESTRICTED"] == "0"))
    @MainActor func realBackendStreamingDownloadRequestBudget() async throws {
        let env = ProcessInfo.processInfo.environment
        let origin = try ServerOrigin(#require(env["CLOUDNEST_SWIFT_ORIGIN"]))
        let policy = try FixtureTLSDelegate(der: Data(contentsOf: URL(fileURLWithPath: #require(env["CLOUDNEST_SWIFT_CERTIFICATE"]))))
        let configuration = URLSessionConfiguration.ephemeral
        configuration.httpAdditionalHeaders = ["X-CloudNest-Test-Stream": "native-single-stream"]
        let transport = URLSession(configuration: configuration, delegate: policy, delegateQueue: nil)
        defer { transport.invalidateAndCancel() }
        let state = SessionState(api: ServerAPI(session: transport, delegate: policy), vault: StreamTestVault())
        await state.restore()
        await state.signIn(server: origin.url.absoluteString, username: try #require(env["CLOUDNEST_SWIFT_USERNAME"]),
                           password: try #require(env["CLOUDNEST_SWIFT_PASSWORD"]))
        let root = FileManager.default.temporaryDirectory.appending(path: "cloudnest-stream-\(UUID())")
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let source = root.appending(path: "stream-\(UUID()).bin")
        let payload = Data(repeating: 0x47, count: 9 * 1024 * 1024 + 173)
        try payload.write(to: source)
        let manager = TransferManager(session: state, directory: root.appending(path: "journal"))
        let upload = try await manager.enqueueUpload(source, parentID: 0)
        await manager.waitUntilIdle(upload)
        #expect(manager.records[upload]?.phase == .completed)
        let fileID = try #require(manager.records[upload]?.resultFileID)
        let file = try #require(try await state.listFiles(parentID: 0, recycleBin: false).first { $0.id == fileID })
        let download = try await manager.enqueueDownload(file, destination: root.appending(path: "result.bin"))
        await manager.waitUntilIdle(download)
        #expect(manager.records[download]?.phase == .completed)
        let result = try await manager.resultURL(download)
        #expect(try Data(contentsOf: result) == payload)
        var check = URLRequest(url: origin.endpoint("api/auth/me"), cachePolicy: .reloadIgnoringLocalCacheData)
        check.setValue("Bearer \(try state.transferContext().token)", forHTTPHeaderField: "Authorization")
        let (_, response) = try await transport.data(for: check)
        let counter = try #require(response as? HTTPURLResponse)
        #expect(counter.statusCode == 200)
        #expect(counter.value(forHTTPHeaderField: "X-CloudNest-Test-Download-Requests") == "1")
        await manager.close()
        await state.signOut()
        // The real Go wrapper independently asserts exactly one request for
        // this uninterrupted download, not one full pre-hash per disk block.
    }
}
