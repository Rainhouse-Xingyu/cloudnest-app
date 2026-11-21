import Foundation
import Testing
@testable import CloudNestCore

private final class FRPStateVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ session: SavedSession) throws { saved = session }
    func clear() throws { saved = nil }
}

private final class FRPResponseGate: @unchecked Sendable {
    private let lock = NSLock()
    private var request: FRPStateProtocol?
    var waiting: Bool { lock.lock(); defer { lock.unlock() }; return request != nil }
    func hold(_ request: FRPStateProtocol) { lock.lock(); self.request = request; lock.unlock() }
    func release() {
        lock.lock(); let request = self.request; self.request = nil; lock.unlock()
        request?.respond(401, #"{"error":"expired"}"#)
    }
}

private final class FRPStateProtocol: URLProtocol, @unchecked Sendable {
    static let gate = FRPResponseGate()
    private let lock = NSLock()
    private var stopped = false
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        switch request.url?.path {
        case "/api/auth/login":
            respond(200, "{\"token\":\"\(UUID())\",\"token_type\":\"Bearer\",\"user\":{\"id\":17,\"username\":\"frp\",\"email\":null,\"is_admin\":false}}")
        case "/api/auth/logout", "/api/resource/health": respond(200, #"{"message":"ok"}"#)
        case "/api/frp/devices/1/status": Self.gate.hold(self)
        case "/api/frp/devices/1/mappings":
            respond(200, #"{"items":[{"id":9,"device_id":1,"server_id":2,"local_port":8080,"remote_port":32000,"domain":"","status":1}]}"#)
        case "/api/frp/devices/1/configs":
            // A misbound response must not display another device's metadata.
            respond(200, #"{"items":[{"id":3,"device_id":7,"server_id":2,"server_revision_id":4,"create_time":"2026-11-21T00:00:00Z"}]}"#)
        default: respond(403, #"{"error":"forbidden"}"#)
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

@Suite(.serialized)
struct FRPStateTests {
    @Test @MainActor func switchingSectionsCancelsOldRequestsAndRejectsMisboundMetadata() async throws {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [FRPStateProtocol.self]
        let session = SessionState(api: ServerAPI(configuration: configuration), vault: FRPStateVault())
        await session.restore()
        await session.signIn(server: "https://frp-state.test", username: "fixture", password: "fixture")
        let device = FRPDevice(id: 1, userID: 17, name: "Fixture", address: "", status: 1)
        let detail = FRPDetailState(device: device, session: session)
        let slow = Task { await detail.load(.status) }
        let deadline = ContinuousClock.now.advanced(by: .seconds(3))
        while !FRPStateProtocol.gate.waiting {
            guard ContinuousClock.now < deadline else { throw ServerError.unavailable }
            try await Task.sleep(for: .milliseconds(5))
        }
        await detail.load(.mappings)
        FRPStateProtocol.gate.release()
        await slow.value
        guard case .mappings(let mappings) = detail.snapshot else { Issue.record("Old request replaced mappings"); return }
        #expect(mappings.map(\.id) == [9])
        #expect(detail.error == nil)
        #expect(session.account != nil)
        await detail.load(.configs)
        #expect(detail.snapshot == nil)
        #expect(detail.error == ServerError.invalidResponse.localizedDescription)
        await session.signOut()
        await session.signIn(server: "https://other-frp-state.test", username: "fixture", password: "fixture")
        await detail.load(.mappings)
        #expect(detail.snapshot == nil)
        #expect(detail.error == ServerError.sessionChanged.localizedDescription)
        #expect(session.account != nil)
    }
}
