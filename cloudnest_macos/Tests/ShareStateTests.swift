import Foundation
import Testing
@testable import CloudNestCore

private final class ShareStateVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ value: SavedSession) throws { saved = value }
    func clear() throws { saved = nil }
}
private final class ShareGate: @unchecked Sendable {
    private let lock = NSLock()
    private var counts: [String: Int] = [:]
    private var held: ShareProtocol?
    var waiting: Bool { lock.lock(); defer { lock.unlock() }; return held != nil }
    func next(_ key: String) -> Int { lock.lock(); defer { lock.unlock() }; counts[key, default: 0] += 1; return counts[key]! }
    func hold(_ request: ShareProtocol) { lock.lock(); held = request; lock.unlock() }
    func release() { lock.lock(); let request = held; held = nil; lock.unlock(); request?.respond(401, "{}") }
}
private final class ShareProtocol: URLProtocol, @unchecked Sendable {
    static let gate = ShareGate()
    private let lock = NSLock()
    private var stopped = false
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let host = request.url?.host ?? ""
        switch request.url?.path {
        case "/api/auth/login": respond(200, "{\"token\":\"\(UUID())\",\"token_type\":\"Bearer\",\"user\":{\"id\":17,\"username\":\"fixture\",\"is_admin\":false}}")
        case "/api/auth/logout", "/api/resource/health": respond(200, #"{"message":"ok"}"#)
        case "/api/shares":
            let n = Self.gate.next(host)
            if host == "cancel.shares.test" && n == 1 { Self.gate.hold(self); return }
            let row = #"{"id":1,"file_id":8,"file_name":"Fixture.txt","user_id":17,"share_code":"Abcdef123456","expire_time":null,"create_time":"2026-11-22T00:00:00Z","requires_passcode":false,"max_downloads":0,"download_count":0}"#
            if host == "denied.shares.test" && n > 1 { respond(403, "{}") }
            else if host == "duplicate.shares.test" && n > 1 { respond(200, "{\"items\":[\(row),\(row)]}") }
            else { respond(200, "{\"items\":[\(row)]}") }
        case "/api/files/8/share":
            // A successful HTTP response must not send the user to another
            // origin or drop the requested protection/limit silently.
            respond(200, #"{"share_code":"Abcdef123456","expire_time":null,"requires_passcode":true,"max_downloads":1,"access_url":"https://other.test/capture"}"#)
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
struct ShareStateTests {
    @MainActor private func session(host: String) async -> SessionState {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [ShareProtocol.self]
        let session = SessionState(api: ServerAPI(configuration: configuration), vault: ShareStateVault())
        await session.restore()
        await session.signIn(server: "https://\(host)", username: "fixture", password: "fixture")
        return session
    }

    @Test @MainActor func deniedAndMalformedListsClearCapabilityRows() async {
        for host in ["denied.shares.test", "duplicate.shares.test"] {
            let session = await session(host: host)
            let state = SharesState(session: session)
            await state.load()
            #expect(state.items.count == 1 && state.error == nil)
            await state.load()
            #expect(state.items.isEmpty && state.error != nil && session.account != nil)
            state.cancel()
        }
    }

    @Test @MainActor func replacingShareListCancelsTheOldUnauthorizedResult() async throws {
        let session = await session(host: "cancel.shares.test")
        let state = SharesState(session: session)
        let first = Task { await state.load() }
        let deadline = ContinuousClock.now.advanced(by: .seconds(3))
        while !ShareProtocol.gate.waiting {
            guard ContinuousClock.now < deadline else { throw ServerError.unavailable }
            try await Task.sleep(for: .milliseconds(5))
        }
        await state.load()
        ShareProtocol.gate.release()
        await first.value
        #expect(state.items.count == 1 && state.error == nil && session.account != nil)
        state.cancel()
        #expect(state.items.isEmpty && !state.busy)
    }

    @Test @MainActor func creationCannotAcceptAnOffOriginLinkAndCodesCannotEscapePaths() async throws {
        let session = await session(host: "receipt.shares.test")
        let file = FileItem(id: 8, filename: "Fixture.txt", parentID: 0, isFolder: 0, size: 1, currentVersion: 1, updateTime: "", fileType: "text/plain", userID: 17)
        do {
            _ = try await session.createShare(file: file, expireHours: 0, passcode: "fixture-code", maxDownloads: 1, expectedIdentity: session.transferIdentity)
            Issue.record("Off-origin share link was accepted")
        } catch { #expect(error as? ServerError == .invalidResponse) }
        let origin = try ServerOrigin("https://receipt.shares.test")
        for code in ["../secret", "a?token=secret", "a#fragment", "a/b", "", String(repeating: "a", count: 21)] {
            do {
                _ = try ShareContract.url(origin: origin, code: code)
                Issue.record("Unsafe share code became a URL")
            } catch { #expect(error as? ServerError == .invalidOperation) }
        }
    }
}
