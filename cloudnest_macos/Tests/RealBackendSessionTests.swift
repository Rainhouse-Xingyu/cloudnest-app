import Foundation
import Security
import Testing
@testable import CloudNestCore

private final class FixtureTLSDelegate: NSObject, URLSessionDelegate, URLSessionTaskDelegate, @unchecked Sendable {
    let certificate: SecCertificate

    init(der: Data) throws {
        certificate = try #require(SecCertificateCreateWithData(nil, der as CFData))
    }

    func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge,
                    completionHandler: @escaping @Sendable (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
              let trust = challenge.protectionSpace.serverTrust,
              SecTrustSetAnchorCertificates(trust, [certificate] as CFArray) == errSecSuccess,
              SecTrustSetAnchorCertificatesOnly(trust, true) == errSecSuccess,
              SecTrustEvaluateWithError(trust, nil) else {
            completionHandler(.cancelAuthenticationChallenge, nil)
            return
        }
        completionHandler(.useCredential, URLCredential(trust: trust))
    }

    func urlSession(_ session: URLSession, task: URLSessionTask,
                    willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest) async -> URLRequest? { nil }
}

@Test(.enabled(if: ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_ORIGIN"] != nil))
@MainActor func realBackendSessionLifecycle() async throws {
    let env = ProcessInfo.processInfo.environment
    let origin = try ServerOrigin(#require(env["CLOUDNEST_SWIFT_ORIGIN"]))
    let username = try #require(env["CLOUDNEST_SWIFT_USERNAME"])
    let password = try #require(env["CLOUDNEST_SWIFT_PASSWORD"])
    let certificateURL = URL(fileURLWithPath: try #require(env["CLOUDNEST_SWIFT_CERTIFICATE"]))
    let delegate = try FixtureTLSDelegate(der: Data(contentsOf: certificateURL))
    let configuration = URLSessionConfiguration.ephemeral
    configuration.httpCookieStorage = nil
    configuration.urlCredentialStorage = nil
    configuration.urlCache = nil
    configuration.timeoutIntervalForRequest = 15
    configuration.timeoutIntervalForResource = 30
    let transport = URLSession(configuration: configuration, delegate: delegate, delegateQueue: nil)
    defer { transport.invalidateAndCancel() }
    let api = ServerAPI(session: transport)
    let vault = KeychainSessionVault(service: "org.cloudnest.macos.real-test.\(UUID())")
    defer { try? vault.clear() }
    let state = SessionState(api: api, vault: vault)
    await state.restore()
    await state.signIn(server: origin.url.absoluteString, username: username, password: "incorrect-fixture-password")
    #expect(state.account == nil)
    #expect(state.error == ServerError.signInRejected.localizedDescription)
    await state.signIn(server: origin.url.absoluteString, username: username, password: password)
    #expect(state.error == nil)
    #expect(state.account?.username == username)
    let expected: SessionState.Connection = env["CLOUDNEST_SWIFT_RESTRICTED"] == "1" ? .restricted : .connected
    #expect(state.connection == expected)
    let saved = try #require(try vault.read())

    let reopened = SessionState(api: api, vault: vault)
    await reopened.restore()
    #expect(reopened.account == state.account)
    #expect(reopened.connection == expected)
    await reopened.refresh()
    #expect(reopened.connection == expected)
    await reopened.signOut()
    #expect(reopened.error == nil)
    #expect(reopened.account == nil)
    let vaultIsEmpty = try vault.read() == nil
    #expect(vaultIsEmpty)
    do {
        _ = try await api.profile(origin, token: saved.token)
        Issue.record("The Go server accepted a revoked session")
    } catch {
        #expect(error as? ServerError == .unauthorized)
    }
    // A second window with the old token must observe revocation on refresh.
    await state.refresh()
    #expect(state.account == nil)
    #expect(!state.hasSavedSession)
}
