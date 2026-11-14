import Foundation
import Testing
@testable import CloudNestCore

private final class FixtureProtocol: URLProtocol, @unchecked Sendable {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        guard let url = request.url else { return }
        let path = url.path
        let bearer = request.value(forHTTPHeaderField: "Authorization")
        let fixtureUser = #"{"id":17,"username":"fixture","email":null,"is_admin":false}"#
        var status = 200
        var body = #"{"message":"ok"}"#
        switch path {
        case "/api/auth/login":
            if request.httpMethod != "POST" || bearer != nil {
                status = 400
            }
            body = #"{"token":"fixture-token","token_type":"Bearer","user":"# + fixtureUser + "}"
        case "/api/auth/me":
            if bearer == "Bearer fixture-token" {
                body = #"{"user":"# + fixtureUser + "}"
            } else {
                status = 401
            }
        case "/api/resource/health":
            status = bearer == "Bearer fixture-token" ? 403 : 401
        case "/api/auth/logout":
            if request.httpMethod != "POST" || bearer != "Bearer fixture-token" {
                status = 400
            } else if url.host == "logout-unavailable.test" {
                status = 503
            }
        default:
            status = 404
        }
        let response = HTTPURLResponse(url: url, statusCode: status, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(body.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

private final class MemoryVault: SessionVault {
    var saved: SavedSession?
    init(_ saved: SavedSession? = nil) { self.saved = saved }
    func read() throws -> SavedSession? { saved }
    func write(_ session: SavedSession) throws { saved = session }
    func clear() throws { saved = nil }
}

private func fixtureAPI() -> ServerAPI {
    let configuration = URLSessionConfiguration.ephemeral
    configuration.protocolClasses = [FixtureProtocol.self]
    return ServerAPI(configuration: configuration)
}

@Test func serverOriginRejectsCredentialsAndNonHTTPS() throws {
    let origin = try ServerOrigin(" HTTPS://CloudNest.Example:443/ ")
    #expect(origin.url.absoluteString == "https://cloudnest.example")
    #expect(origin.endpoint("api/auth/me").path == "/api/auth/me")
    for invalid in ["http://127.0.0.1", "https://name:secret@example.com", "https://example.com/base",
                    "https://example.com?token=value", "https://example.com#fragment"] {
        #expect(throws: ServerError.invalidAddress) { try ServerOrigin(invalid) }
    }
}

@Test @MainActor func restrictedHealthKeepsSessionAndLogoutClearsVault() async throws {
    let vault = MemoryVault()
    let state = SessionState(api: fixtureAPI(), vault: vault)
    await state.restore()
    await state.signIn(server: "https://cloudnest.test", username: "fixture", password: "fixture")
    #expect(state.account?.id == 17)
    #expect(state.connection == .restricted)
    #expect(vault.saved?.server == "https://cloudnest.test")

    let reopened = SessionState(api: fixtureAPI(), vault: vault)
    await reopened.restore()
    #expect(reopened.account?.id == 17)
    #expect(reopened.connection == .restricted)
    await reopened.signOut()
    #expect(reopened.account == nil)
    #expect(vault.saved == nil)
}

@Test @MainActor func revokedSessionIsRemovedOnRestore() async {
    let vault = MemoryVault(SavedSession(server: "https://cloudnest.test", token: "revoked-fixture"))
    let state = SessionState(api: fixtureAPI(), vault: vault)
    await state.restore()
    #expect(state.restored)
    #expect(state.account == nil)
    #expect(vault.saved == nil)
    #expect(state.error == ServerError.unauthorized.localizedDescription)
}

@Test @MainActor func unavailableLogoutPreservesSessionForRetry() async {
    let vault = MemoryVault(SavedSession(server: "https://logout-unavailable.test", token: "fixture-token"))
    let state = SessionState(api: fixtureAPI(), vault: vault)
    await state.restore()
    await state.signOut()
    #expect(state.account?.id == 17)
    #expect(vault.saved?.token == "fixture-token")
    #expect(state.connection == .unavailable)
    #expect(state.error == ServerError.unavailable.localizedDescription)
}

@Test(.enabled(if: ProcessInfo.processInfo.environment["CLOUDNEST_TEST_KEYCHAIN"] == "1"))
func keychainRoundTripUsesAnIsolatedItem() throws {
    let vault = KeychainSessionVault(service: "org.cloudnest.macos.test.\(UUID())")
    defer { try? vault.clear() }
    let first = SavedSession(server: "https://example.test", token: "fixture-one")
    let second = SavedSession(server: "https://example.test", token: "fixture-two")
    #expect(try vault.read() == nil)
    try vault.write(first)
    #expect(try vault.read() == first)
    try vault.write(second)
    #expect(try vault.read() == second)
    try vault.clear()
    #expect(try vault.read() == nil)
}
