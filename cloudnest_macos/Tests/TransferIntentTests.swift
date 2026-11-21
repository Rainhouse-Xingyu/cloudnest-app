import Foundation
import Testing
@testable import CloudNestCore

private final class IntentVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ session: SavedSession) throws { saved = session }
    func clear() throws { saved = nil }
}

private final class IntentResponseGate: @unchecked Sendable {
    private let lock = NSLock()
    private var pending: IntentProtocol?
    func hold(_ request: IntentProtocol) { lock.lock(); pending = request; lock.unlock() }
    var waiting: Bool { lock.lock(); defer { lock.unlock() }; return pending != nil }
    func release(status: Int) {
        lock.lock(); let request = pending; pending = nil; lock.unlock()
        request?.respond(status: status, body: #"{"items":[]}"#)
    }
}

private final class IntentProtocol: URLProtocol, @unchecked Sendable {
    static let gate = IntentResponseGate()
    private let lock = NSLock()
    private var stopped = false
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        guard let url = request.url else { return }
        switch url.path {
        case "/api/auth/login":
            respond(status: 200, body: "{\"token\":\"\(UUID().uuidString)\",\"token_type\":\"Bearer\",\"user\":{\"id\":17,\"username\":\"intent\",\"email\":null,\"is_admin\":false}}")
        case "/api/auth/logout": respond(status: 200, body: #"{"message":"ok"}"#)
        case "/api/resource/health": respond(status: 403, body: #"{"error":"restricted"}"#)
        case "/api/files": Self.gate.hold(self)
        case "/api/files/upload/init" where url.host == "blocked-intent.test": Self.gate.hold(self)
        default: respond(status: 404, body: #"{"error":"not found"}"#)
        }
    }
    func respond(status: Int, body: String) {
        lock.lock(); let cancelled = stopped; lock.unlock()
        guard !cancelled else { return }
        guard let url = request.url, let response = HTTPURLResponse(url: url, statusCode: status, httpVersion: nil, headerFields: nil) else { return }
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(body.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() { lock.lock(); stopped = true; lock.unlock() }
}

@MainActor private func intentSession() async -> SessionState {
    let configuration = URLSessionConfiguration.ephemeral
    configuration.protocolClasses = [IntentProtocol.self]
    let state = SessionState(api: ServerAPI(configuration: configuration), vault: IntentVault())
    await state.restore()
    await state.signIn(server: "https://first-intent.test", username: "fixture", password: "fixture")
    return state
}

@Suite(.serialized)
struct TransferIntentTests {
    @Test @MainActor func closeWaitsForAControlAlreadyResolvingItsUpload() async throws {
        let state = await intentSession()
        await state.signOut()
        await state.signIn(server: "https://blocked-intent.test", username: "fixture", password: "fixture")
        let root = FileManager.default.temporaryDirectory.appending(path: "cloudnest-control-close-\(UUID())")
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let source = root.appending(path: "unknown-upload.txt")
        try Data("No initialization receipt.".utf8).write(to: source)
        let journal = root.appending(path: "journal")
        let manager = TransferManager(session: state, directory: journal)
        let original = try #require(state.transferIdentity)
        let id = try await manager.enqueueUpload(source, parentID: 0, expectedIdentity: original)
        func waitForRequest() async throws {
            let deadline = ContinuousClock.now.advanced(by: .seconds(3))
            while !IntentProtocol.gate.waiting {
                guard ContinuousClock.now < deadline else { throw ServerError.unavailable }
                try await Task.sleep(for: .milliseconds(5))
            }
        }
        try await waitForRequest()
        IntentProtocol.gate.release(status: 503)
        await manager.waitUntilIdle(id)
        #expect(manager.records[id]?.phase == .failed)
        let control = Task { await manager.pause(id) }
        try await waitForRequest()
        #expect(manager.records[id]?.phase == .pausing)
        await state.signOut()
        await state.signIn(server: "https://second-intent.test", username: "fixture", password: "fixture")
        var began = false
        var finished = false
        let closing = Task { began = true; await manager.close(); finished = true }
        while !began { await Task.yield() }
        #expect(!finished)
        var secondBegan = false
        var secondFinished = false
        let secondClose = Task { secondBegan = true; await manager.close(); secondFinished = true }
        while !secondBegan { await Task.yield() }
        #expect(!secondFinished)
        do {
            _ = try await manager.enqueueUpload(source, parentID: 0, expectedIdentity: state.transferIdentity)
            Issue.record("A new admission started while its journal was closing")
        } catch { #expect(error as? TransferError == .localStorage) }
        IntentProtocol.gate.release(status: 503)
        await control.value
        await closing.value
        await secondClose.value
        #expect(finished)
        #expect(secondFinished)
        let reopened = TransferManager(session: state, directory: journal)
        await reopened.restore()
        #expect(reopened.restored)
        #expect(reopened.records[id]?.identity == original)
        #expect(reopened.records[id]?.phase == .failed)
        #expect(reopened.jobs.isEmpty)
        await reopened.close()
    }

    @Test @MainActor func closingAfterAccountSwitchWaitsForTheOldWorkers() async throws {
        let state = await intentSession()
        await state.signOut()
        await state.signIn(server: "https://blocked-intent.test", username: "fixture", password: "fixture")
        let original = try #require(state.transferIdentity)
        let root = FileManager.default.temporaryDirectory.appending(path: "cloudnest-close-intent-\(UUID())")
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let source = root.appending(path: "blocked.txt")
        try Data("A worker waiting for its first response.".utf8).write(to: source)
        let journal = root.appending(path: "journal")
        let manager = TransferManager(session: state, directory: journal)
        let id = try await manager.enqueueUpload(source, parentID: 0, expectedIdentity: original)
        let deadline = ContinuousClock.now.advanced(by: .seconds(3))
        while !IntentProtocol.gate.waiting {
            guard ContinuousClock.now < deadline else { throw ServerError.unavailable }
            try await Task.sleep(for: .milliseconds(5))
        }
        await state.signOut()
        await state.signIn(server: "https://second-intent.test", username: "fixture", password: "fixture")
        await manager.close()
        #expect(manager.records[id]?.phase == .paused)
        // Release the test gate even for an old implementation that failed to
        // join its worker, so failure does not leak a hanging test request.
        IntentProtocol.gate.release(status: 503)
        await manager.waitUntilIdle(id)
        let reopened = TransferManager(session: state, directory: journal)
        await reopened.restore()
        #expect(reopened.restored)
        #expect(reopened.records[id]?.phase == .paused)
        #expect(reopened.records[id]?.identity == original)
        #expect(reopened.jobs.isEmpty)
        await reopened.close()
    }

    @Test @MainActor func staleSelectionsCannotEnqueueIntoAnotherServer() async throws {
        let state = await intentSession()
        let intent = try #require(state.transferIdentity)
        let root = FileManager.default.temporaryDirectory.appending(path: "cloudnest-intent-\(UUID())")
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let source = root.appending(path: "selected.txt")
        try Data("selected in the first account".utf8).write(to: source)
        let file = try JSONDecoder().decode(FileItem.self, from: Data(#"{"id":1,"filename":"target.txt","parent_id":0,"is_folder":0,"size":1,"current_version":1,"update_time":"2026-11-20T00:00:00Z","file_type":"txt"}"#.utf8))
        let version = try JSONDecoder().decode(FileVersionItem.self, from: Data(#"{"id":1,"version":1,"size":1,"comment":"","username":"","create_time":"2026-11-20T00:00:00Z"}"#.utf8))
        let manager = TransferManager(session: state, directory: root.appending(path: "journal"))
        let oldBrowser = FileBrowserState(session: state, recycleBin: false)
        await manager.restore()
        await state.signOut()
        await state.signIn(server: "https://second-intent.test", username: "fixture", password: "fixture")
        #expect(state.transferIdentity != intent)
        for operation in 0..<5 {
            do {
                switch operation {
                case 0: _ = try await manager.enqueueUpload(source, parentID: 0, expectedIdentity: intent)
                case 1: _ = try await manager.enqueueDownload(file, destination: root.appending(path: "download.txt"), expectedIdentity: intent)
                case 2: _ = try await manager.enqueueVersionUpload(source, file: file, baseVersion: 1, comment: "", expectedIdentity: intent)
                case 3: _ = try await manager.enqueueVersionDownload(file, version: version, destination: root.appending(path: "version.txt"), expectedIdentity: intent)
                default: _ = try await manager.enqueuePreview(file, expectedIdentity: intent)
                }
                Issue.record("A stale file selection was assigned to the new server")
            } catch { #expect(error as? ServerError == .sessionChanged) }
        }
        #expect(manager.records.isEmpty)
        await oldBrowser.createFolder(name: "stale-window")
        #expect(oldBrowser.error == ServerError.sessionChanged.localizedDescription)
        await oldBrowser.rename(file, name: "stale-name.txt")
        #expect(oldBrowser.error == ServerError.sessionChanged.localizedDescription)
        await oldBrowser.relocate(ids: [file.id], parentID: 0, copy: true)
        #expect(oldBrowser.error == ServerError.sessionChanged.localizedDescription)
        await oldBrowser.reload()
        #expect(oldBrowser.items.isEmpty)
        #expect(oldBrowser.error == ServerError.sessionChanged.localizedDescription)
        await manager.close()
    }

    @Test @MainActor func lateResponsesCannotReplaceOrInvalidateANewSession() async throws {
        for destination in ["https://second-intent.test", "https://first-intent.test"] {
            for status in [200, 401] {
                let state = await intentSession()
                let old = try #require(state.transferIdentity)
                let request = Task { try await state.listFiles(parentID: 0, recycleBin: false) }
                let deadline = ContinuousClock.now.advanced(by: .seconds(3))
                while !IntentProtocol.gate.waiting {
                    guard ContinuousClock.now < deadline else { throw ServerError.unavailable }
                    try await Task.sleep(for: .milliseconds(5))
                }
                await state.signOut()
                await state.signIn(server: destination, username: "fixture", password: "fixture")
                let current = try #require(state.transferIdentity)
                #expect((current == old) == (destination == "https://first-intent.test"))
                IntentProtocol.gate.release(status: status)
                do { _ = try await request.value; Issue.record("A previous session's response was returned to a new session") }
                catch { #expect(error is CancellationError) }
                #expect(state.transferIdentity == current)
            }
        }
    }

    @Test @MainActor func oldTransferUnauthorizedCannotRevokeAFreshLoginToTheSameAccount() async throws {
        let state = await intentSession()
        let old = try state.transferContext()
        await state.signOut()
        await state.signIn(server: old.origin.url.absoluteString, username: "fixture", password: "fixture")
        let current = try state.transferContext()
        #expect(current.identity == old.identity)
        #expect(current.token != old.token)
        state.observeTransferFailure(ServerError.unauthorized, context: old)
        #expect(state.hasSavedSession)
        #expect(state.transferIdentity == current.identity)
        state.observeTransferFailure(ServerError.unauthorized, context: current)
        #expect(!state.hasSavedSession)
    }
}
