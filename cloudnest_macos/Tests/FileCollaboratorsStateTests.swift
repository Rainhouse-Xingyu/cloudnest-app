import Foundation
import Testing
@testable import CloudNestCore

private final class CollaboratorsStateVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ value: SavedSession) throws { saved = value }
    func clear() throws { saved = nil }
}

private final class CollaboratorsGate: @unchecked Sendable {
    private let lock = NSLock()
    private var counts: [String: Int] = [:]
    private var held: CollaboratorsProtocol?
    var waiting: Bool { lock.lock(); defer { lock.unlock() }; return held != nil }
    func next(_ key: String) -> Int { lock.lock(); defer { lock.unlock() }; counts[key, default: 0] += 1; return counts[key]! }
    func hold(_ request: CollaboratorsProtocol) { lock.lock(); held = request; lock.unlock() }
    func release() { lock.lock(); let request = held; held = nil; lock.unlock(); request?.respond(401, "{}") }
}

private final class CollaboratorsProtocol: URLProtocol, @unchecked Sendable {
    static let gate = CollaboratorsGate()
    private let lock = NSLock()
    private var stopped = false
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let path = request.url?.path ?? ""
        switch path {
        case "/api/auth/login": respond(200, "{\"token\":\"\(UUID())\",\"token_type\":\"Bearer\",\"user\":{\"id\":17,\"username\":\"fixture\",\"is_admin\":false}}")
        case "/api/auth/logout", "/api/resource/health": respond(200, #"{"message":"ok"}"#)
        case "/api/files/1/collaborators", "/api/files/2/collaborators":
            let n = Self.gate.next(path)
            if path == "/api/files/2/collaborators" && n == 1 { Self.gate.hold(self) }
            else if n <= 2 && path == "/api/files/2/collaborators" || n == 1 {
                respond(200, #"{"items":[{"user_id":24,"username":"peer","permission":"read"},{"user_id":24,"username":"peer","permission":"write"}]}"#)
            } else { respond(403, "{}") }
        case "/api/files/3/collaborators":
            respond(200, #"{"items":[{"user_id":24,"username":"peer","permission":"read"},{"user_id":24,"username":"different-account","permission":"write"}]}"#)
        default: respond(200, #"{"items":[{"user_id":24,"username":"peer","permission":"owner"}]}"#)
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
struct FileCollaboratorsStateTests {
    @MainActor private func session() async -> SessionState {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [CollaboratorsProtocol.self]
        let session = SessionState(api: ServerAPI(configuration: configuration), vault: CollaboratorsStateVault())
        await session.restore()
        await session.signIn(server: "https://collaborators.test", username: "fixture", password: "fixture")
        return session
    }
    private func file(_ id: Int64) -> FileItem {
        FileItem(id: id, filename: "Fixture.txt", parentID: 0, isFolder: 0, size: 1, currentVersion: 1, updateTime: "", fileType: "text/plain", userID: 17)
    }

    @Test @MainActor func legacyGrantUnionAndDeniedRefreshClearNamesWithoutLoggingOut() async throws {
        let session = await session()
        let state = FileCollaboratorsState(session: session, file: file(1))
        await state.load()
        #expect(state.items == [FileCollaborator(fileID: 1, userID: 24, username: "peer", permission: .write)])
        await state.load()
        #expect(state.items.isEmpty && state.error == ServerError.forbidden.localizedDescription)
        #expect(session.account != nil)
        state.cancel()
    }

    @Test @MainActor func replacingARequestCannotPublishItsOldUnauthorizedResult() async throws {
        let session = await session()
        let state = FileCollaboratorsState(session: session, file: file(2))
        let first = Task { await state.load() }
        let deadline = ContinuousClock.now.advanced(by: .seconds(3))
        while !CollaboratorsProtocol.gate.waiting {
            guard ContinuousClock.now < deadline else { throw ServerError.unavailable }
            try await Task.sleep(for: .milliseconds(5))
        }
        await state.load()
        CollaboratorsProtocol.gate.release()
        await first.value
        #expect(state.items.first?.permission == .write && state.error == nil && session.account != nil)
        state.cancel()
        #expect(state.items.isEmpty && !state.busy)
    }

    @Test @MainActor func contradictoryOrUnknownServerPermissionsFailClosed() async {
        let session = await session()
        for id: Int64 in [3, 4] {
            let state = FileCollaboratorsState(session: session, file: file(id))
            await state.load()
            #expect(state.items.isEmpty && state.error != nil && session.account != nil)
            state.cancel()
        }
    }
}
