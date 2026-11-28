import Foundation
import Testing
@testable import CloudNestCore

private final class CommandVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ session: SavedSession) throws { saved = session }
    func clear() throws { saved = nil }
}

private final class CommandGate: @unchecked Sendable {
    private let lock = NSLock()
    private var pending: CommandProtocol?
    private var directory: URL?
    private var requests = 0
    private var persisted = false
    func reset(_ directory: URL) { lock.lock(); self.directory = directory; pending = nil; requests = 0; persisted = false; lock.unlock() }
    var snapshot: (waiting: Bool, count: Int, persisted: Bool) {
        lock.lock(); defer { lock.unlock() }; return (pending != nil, requests, persisted)
    }
    func hold(_ request: CommandProtocol, requestID: String) {
        lock.lock(); defer { lock.unlock() }
        requests += 1
        pending = request
        if let directory, let paths = try? FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil) {
            persisted = paths.filter { $0.pathExtension == "json" }.contains { path in
                guard let bytes = try? Data(contentsOf: path), let record = try? JSONDecoder().decode(FRPCommandIntent.self, from: bytes) else { return false }
                return record.requestID == requestID && record.outcome == .unconfirmed
            }
        }
    }
    func release() {
        lock.lock(); let value = pending; pending = nil; lock.unlock()
        value?.respond(401, #"{"error":"expired"}"#)
    }
}

private final class CommandProtocol: URLProtocol, @unchecked Sendable {
    static let gate = CommandGate()
    private let lock = NSLock()
    private var stopped = false
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        switch request.url?.path {
        case "/api/auth/login":
            respond(200, "{\"token\":\"\(UUID())\",\"token_type\":\"Bearer\",\"user\":{\"id\":17,\"username\":\"commands\",\"email\":null,\"is_admin\":false}}")
        case "/api/auth/logout", "/api/resource/health": respond(200, #"{"message":"ok"}"#)
        case "/api/frp/devices/29/commands":
            var bytes = request.httpBody ?? Data()
            if let stream = request.httpBodyStream {
                stream.open(); defer { stream.close() }
                var buffer = [UInt8](repeating: 0, count: 8192)
                while stream.hasBytesAvailable && bytes.count <= 8192 {
                    let count = stream.read(&buffer, maxLength: buffer.count)
                    if count <= 0 { break }
                    bytes.append(contentsOf: buffer.prefix(count))
                }
            }
            let body = (try? JSONSerialization.jsonObject(with: bytes)) as? [String: Any]
            Self.gate.hold(self, requestID: body?["request_id"] as? String ?? "")
        default: respond(404, #"{"error":"not found"}"#)
        }
    }
    func respond(_ status: Int, _ body: String) {
        lock.lock(); let cancelled = stopped; lock.unlock()
        guard !cancelled, let url = request.url else { return }
        let response = HTTPURLResponse(url: url, statusCode: status, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(body.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() { lock.lock(); stopped = true; lock.unlock() }
}

@MainActor private func commandSession() async -> SessionState {
    let config = URLSessionConfiguration.ephemeral
    config.protocolClasses = [CommandProtocol.self]
    let state = SessionState(api: ServerAPI(configuration: config), vault: CommandVault())
    await state.restore()
    await state.signIn(server: "https://command-one.test", username: "fixture", password: "fixture")
    return state
}

@Suite(.serialized)
struct FRPCommandManagerTests {
    @Test @MainActor func commandIsJournaledBeforeHTTPAndConcurrentCloseRetainsItsKey() async throws {
        let root = FileManager.default.temporaryDirectory.appending(path: "cloudnest-command-close-\(UUID())")
        defer { try? FileManager.default.removeItem(at: root) }
        CommandProtocol.gate.reset(root)
        let state = await commandSession()
        let manager = FRPCommandManager(session: state, directory: root)
        let submission = Task { try await manager.submit(deviceID: 29, action: .diagnose, expectedIdentity: state.transferIdentity) }
        let deadline = ContinuousClock.now.advanced(by: .seconds(3))
        while !CommandProtocol.gate.snapshot.waiting {
            guard ContinuousClock.now < deadline else { throw ServerError.unavailable }
            try await Task.sleep(for: .milliseconds(5))
        }
        #expect(CommandProtocol.gate.snapshot.persisted)
        do {
            _ = try await manager.submit(deviceID: 29, action: .stop, expectedIdentity: state.transferIdentity)
            Issue.record("Two controls reached the same device concurrently")
        } catch { #expect(error as? FRPCommandStoreError == .conflict) }
        async let first: Void = manager.close()
        async let second: Void = manager.close()
        _ = await (first, second)
        let id = try await submission.value
        let original = try #require(manager.records[id])
        #expect(original.outcome == .unconfirmed)
        #expect(CommandProtocol.gate.snapshot.count == 1)
        CommandProtocol.gate.release()
        let reopened = FRPCommandManager(session: state, directory: root)
        await reopened.restore()
        #expect(reopened.records[id]?.requestID == original.requestID)
        #expect(CommandProtocol.gate.snapshot.count == 1)
        do {
            _ = try await manager.retry(id)
            Issue.record("A closed manager submitted a command")
        } catch { #expect(error as? FRPCommandStoreError == .closed) }
        await reopened.close()
    }

    @Test @MainActor func brokenJournalPreventsRemoteSubmissionAndPreservesUnknownBytes() async throws {
        let root = FileManager.default.temporaryDirectory.appending(path: "cloudnest-command-invalid-\(UUID())")
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let unknown = root.appending(path: "unknown.txt")
        let bytes = Data("Do not delete or reinterpret.".utf8)
        try bytes.write(to: unknown)
        CommandProtocol.gate.reset(root)
        let state = await commandSession()
        let manager = FRPCommandManager(session: state, directory: root)
        do {
            _ = try await manager.submit(deviceID: 29, action: .diagnose, expectedIdentity: state.transferIdentity)
            Issue.record("A broken journal allowed a remote command")
        } catch { #expect(error as? FRPCommandStoreError == .storage) }
        #expect(CommandProtocol.gate.snapshot.count == 0)
        #expect(try Data(contentsOf: unknown) == bytes)
        await manager.close()
    }

    @Test @MainActor func staleConfirmationCannotSubmitOnAnotherServer() async throws {
        let root = FileManager.default.temporaryDirectory.appending(path: "cloudnest-command-identity-\(UUID())")
        defer { try? FileManager.default.removeItem(at: root) }
        CommandProtocol.gate.reset(root)
        let state = await commandSession()
        let original = try #require(state.transferIdentity)
        let manager = FRPCommandManager(session: state, directory: root)
        await state.signOut()
        await state.signIn(server: "https://command-two.test", username: "fixture", password: "fixture")
        do {
            _ = try await manager.submit(deviceID: 29, action: .diagnose, expectedIdentity: original)
            Issue.record("An old confirmation submitted to a different server")
        } catch { #expect(error as? ServerError == .sessionChanged) }
        #expect(CommandProtocol.gate.snapshot.count == 0)
        #expect(state.account != nil)
        await manager.close()
    }
}
