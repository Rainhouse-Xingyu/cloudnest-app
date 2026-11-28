import Foundation
import Testing
@testable import CloudNestCore

private final class SearchVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ session: SavedSession) throws { saved = session }
    func clear() throws { saved = nil }
}

private final class SearchGate: @unchecked Sendable {
    private let lock = NSLock()
    private var pending: SearchProtocol?
    func hold(_ value: SearchProtocol) { lock.lock(); pending = value; lock.unlock() }
    var waiting: Bool { lock.lock(); defer { lock.unlock() }; return pending != nil }
    func release() {
        lock.lock(); let value = pending; pending = nil; lock.unlock()
        value?.respond(status: 401, body: #"{"error":"expired"}"#)
    }
}

private final class SearchProtocol: URLProtocol, @unchecked Sendable {
    static let gate = SearchGate()
    private let lock = NSLock()
    private var stopped = false
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        switch request.url?.path {
        case "/api/auth/login":
            respond(status: 200, body: "{\"token\":\"\(UUID())\",\"token_type\":\"Bearer\",\"user\":{\"id\":17,\"username\":\"search\",\"email\":null,\"is_admin\":false}}")
        case "/api/auth/logout", "/api/resource/health": respond(status: 200, body: #"{"message":"ok"}"#)
        case "/api/ai/search/nl":
            var body = request.httpBody ?? Data()
            if let stream = request.httpBodyStream {
                stream.open()
                defer { stream.close() }
                var buffer = [UInt8](repeating: 0, count: 8192)
                while stream.hasBytesAvailable && body.count <= 16 * 1024 {
                    let count = stream.read(&buffer, maxLength: buffer.count)
                    if count <= 0 { break }
                    body.append(contentsOf: buffer.prefix(count))
                }
            }
            let fields = (try? JSONSerialization.jsonObject(with: body)) as? [String: Any]
            let question = fields?["question"] as? String ?? ""
            if question == "slow" { Self.gate.hold(self); return }
            if question == "denied" { respond(status: 403, body: #"{"error":"forbidden"}"#); return }
            let hit: [String: Any] = ["id": 1, "filename": question + ".md", "size": 32,
                "parent_id": 0, "user_id": 17, "snippet": "数据库迁移😀原文", "match_sources": ["semantic"],
                "extraction_kind": "text", "index_generation": 2, "snippet_offset_basis": "extracted_text_runes",
                "snippet_start_rune": 7, "chunk_ordinal": 1]
            let result: [String: Any] = ["items": question == "invalid" ? [hit, hit] : [hit],
                "pagination": ["page": fields?["page"] ?? 1, "limit": 20, "total": 25],
                "retrieval": ["mode": "hybrid", "warnings": [], "window_limited": false, "window_size": 200]]
            let data = try! JSONSerialization.data(withJSONObject: result)
            respond(status: 200, body: String(decoding: data, as: UTF8.self))
        default: respond(status: 404, body: #"{"error":"not found"}"#)
        }
    }
    func respond(status: Int, body: String) {
        lock.lock(); let cancelled = stopped; lock.unlock()
        guard !cancelled, let url = request.url else { return }
        let response = HTTPURLResponse(url: url, statusCode: status, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(body.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() { lock.lock(); stopped = true; lock.unlock() }
}

@MainActor private func searchSession() async -> SessionState {
    let configuration = URLSessionConfiguration.ephemeral
    configuration.protocolClasses = [SearchProtocol.self]
    let session = SessionState(api: ServerAPI(configuration: configuration), vault: SearchVault())
    await session.restore()
    await session.signIn(server: "https://search.test", username: "fixture", password: "fixture")
    return session
}

@MainActor private func waitForSearchGate() async throws {
    let deadline = ContinuousClock.now.advanced(by: .seconds(3))
    while !SearchProtocol.gate.waiting {
        guard ContinuousClock.now < deadline else { throw ServerError.unavailable }
        try await Task.sleep(for: .milliseconds(5))
    }
}

@Suite(.serialized)
struct AISearchTests {
    @Test @MainActor func supersededAndCancelledSearchesCannotReplaceNewResults() async throws {
        let session = await searchSession()
        let state = AISearchState(session: session)
        state.question = "slow"
        let slow = Task { await state.submit() }
        try await waitForSearchGate()
        state.question = "new"
        await state.submit()
        SearchProtocol.gate.release()
        await slow.value
        #expect(state.response?.items.first?.filename == "new.md")
        #expect(state.response?.items.first?.snippet == "数据库迁移😀原文")
        #expect(state.error == nil)
        #expect(session.account != nil)
        #expect(state.hasNext)
        state.question = "edited but not submitted"
        await state.go(to: 2)
        #expect(state.response?.pagination.page == 2)
        #expect(state.response?.items.first?.filename == "new.md")
        #expect(!state.hasNext)
        state.question = "slow"
        let cancelled = Task { await state.submit() }
        try await waitForSearchGate()
        state.cancel()
        SearchProtocol.gate.release()
        await cancelled.value
        #expect(!state.busy)
        #expect(state.response == nil)
        #expect(state.error == nil)
        #expect(session.account != nil)
    }

    @Test @MainActor func deniedOrMalformedSearchClearsPreviousSnippets() async throws {
        let session = await searchSession()
        let state = AISearchState(session: session)
        state.question = "valid"
        await state.submit()
        #expect(state.response != nil)
        state.question = "denied"
        await state.submit()
        #expect(state.response == nil)
        #expect(state.error == ServerError.forbidden.localizedDescription)
        #expect(session.account != nil)
        state.question = "invalid"
        await state.submit()
        #expect(state.response == nil)
        #expect(state.error == ServerError.invalidResponse.localizedDescription)
        state.question = String(repeating: "文", count: 1366)
        #expect(!state.canSubmit)
        state.question = " \n "
        #expect(!state.canSubmit)
    }

    @Test @MainActor func searchBoundToAnOldServerCannotQueryANewAccount() async {
        let session = await searchSession()
        let state = AISearchState(session: session)
        await session.signOut()
        await session.signIn(server: "https://other-search.test", username: "fixture", password: "fixture")
        state.question = "valid"
        await state.submit()
        #expect(state.response == nil)
        #expect(state.error == ServerError.sessionChanged.localizedDescription)
        #expect(session.account != nil)
    }
}
