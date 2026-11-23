import Foundation
import Testing
@testable import CloudNestCore

private final class WorkspaceStateVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ value: SavedSession) throws { saved = value }
    func clear() throws { saved = nil }
}

private final class WorkspaceGate: @unchecked Sendable {
    private let lock = NSLock()
    private var held: WorkspaceProtocol?
    private var counts: [String: Int] = [:]
    var waiting: Bool { lock.lock(); defer { lock.unlock() }; return held != nil }
    func count(_ key: String) -> Int { lock.lock(); defer { lock.unlock() }; counts[key, default: 0] += 1; return counts[key]! }
    func hold(_ request: WorkspaceProtocol) { lock.lock(); held = request; lock.unlock() }
    func release() { lock.lock(); let request = held; held = nil; lock.unlock(); request?.respond(401, "{}") }
}

private final class WorkspaceProtocol: URLProtocol, @unchecked Sendable {
    static let gate = WorkspaceGate()
    private let lock = NSLock()
    private var stopped = false
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        switch request.url?.path {
        case "/api/auth/login": respond(200, "{\"token\":\"\(UUID())\",\"token_type\":\"Bearer\",\"user\":{\"id\":17,\"username\":\"fixture\",\"is_admin\":false}}")
        case "/api/auth/logout", "/api/resource/health": respond(200, #"{"message":"ok"}"#)
        case "/api/workspaces":
            let n = Self.gate.count("spaces")
            let row = #"{"id":1,"name":"Workspace fixture","owner_id":17,"root_file_id":9,"role":"owner","create_time":"2026-11-22T00:00:00Z"}"#
            if n == 1 { Self.gate.hold(self) }
            else { respond(200, "{\"items\":[\(row)\(n == 2 ? "" : "," + row)]}") }
        case "/api/workspaces/1/members":
            if Self.gate.count("members") == 1 { respond(200, #"{"items":[{"user_id":17,"username":"fixture","role":"owner"}]}"#) }
            else { respond(403, "{}") }
        default: respond(403, "{}")
        }
    }
    func respond(_ status: Int, _ body: String) {
        lock.lock(); let cancelled = stopped; lock.unlock()
        guard !cancelled else { return }
        let response = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(body.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() { lock.lock(); stopped = true; lock.unlock() }
}

@Suite(.serialized)
struct WorkspaceStateTests {
    @MainActor private func session() async -> SessionState {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [WorkspaceProtocol.self]
        let session = SessionState(api: ServerAPI(configuration: configuration), vault: WorkspaceStateVault())
        await session.restore()
        await session.signIn(server: "https://workspaces.test", username: "fixture", password: "fixture")
        return session
    }

    @Test @MainActor func replacementCancelsOldUnauthorizedAndMalformedListClearsPreviousItems() async throws {
        let session = await session()
        let state = WorkspaceBrowserState(session: session)
        let first = Task { await state.load() }
        let deadline = ContinuousClock.now.advanced(by: .seconds(3))
        while !WorkspaceProtocol.gate.waiting {
            guard ContinuousClock.now < deadline else { throw ServerError.unavailable }
            try await Task.sleep(for: .milliseconds(5))
        }
        await state.load()
        WorkspaceProtocol.gate.release()
        await first.value
        #expect(state.items.map(\.id) == [1] && state.error == nil)
        #expect(session.account != nil)
        await state.load()
        #expect(state.items.isEmpty && state.error == ServerError.invalidResponse.localizedDescription)
        #expect(session.account != nil)
        state.cancel()
    }

    @Test @MainActor func deniedMemberRefreshClearsNamesAndKeepsTheSession() async {
        let session = await session()
        let workspace = Workspace(id: 1, name: "Fixture", ownerID: 17, rootFileID: 9, role: .owner, createdAt: "")
        let state = WorkspaceMembersState(session: session, workspace: workspace)
        await state.load()
        #expect(state.members.map(\.id) == [17])
        await state.load()
        #expect(state.members.isEmpty && state.error == ServerError.forbidden.localizedDescription)
        #expect(session.account != nil)
        state.cancel()
    }
}
