import Foundation
import Testing
@testable import CloudNestCore

private final class EnrollmentVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ value: SavedSession) throws { saved = value }
    func clear() throws { saved = nil }
}

private final class EnrollmentGate: @unchecked Sendable {
    private let lock = NSLock()
    private var held: EnrollmentProtocol?
    var waiting: Bool { lock.lock(); defer { lock.unlock() }; return held != nil }
    func hold(_ request: EnrollmentProtocol) { lock.lock(); held = request; lock.unlock() }
    func release() {
        lock.lock(); let request = held; held = nil; lock.unlock()
        request?.issued(deviceID: 1)
    }
}

private final class EnrollmentProtocol: URLProtocol, @unchecked Sendable {
    static let gate = EnrollmentGate()
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        switch request.url?.path {
        case "/api/auth/login":
            respond(200, "{\"token\":\"\(UUID())\",\"token_type\":\"Bearer\",\"user\":{\"id\":17,\"username\":\"fixture\",\"is_admin\":false}}")
        case "/api/auth/me":
            if request.url?.host == "expired-enrollment.test" { respond(401, "{}") }
            else { respond(200, #"{"user":{"id":17,"username":"fixture","is_admin":false}}"#) }
        case "/api/auth/logout", "/api/resource/health": respond(200, #"{"message":"ok"}"#)
        case "/api/frp/devices/1/enrollment":
            switch request.url?.host {
            case "wrong-enrollment.test", "expired-enrollment.test": respond(401, "{}")
            case "malformed-enrollment.test": issued(deviceID: 2)
            case "held-enrollment.test": Self.gate.hold(self)
            default: issued(deviceID: 1)
            }
        case "/api/frp/devices/1/credential/revoke": respond(204, "")
        default: respond(403, "{}")
        }
    }
    func issued(deviceID: Int) {
        let expiry = ISO8601DateFormatter().string(from: Date().addingTimeInterval(600))
        // Synthetic protocol fixture, not an issued capability.
        let code = "cne1." + String(repeating: "a", count: 32) + "." + String(repeating: "A", count: 43)
        respond(201, "{\"device_id\":\(deviceID),\"enrollment\":\"\(code)\",\"expires_at\":\"\(expiry)\"}")
    }
    func respond(_ status: Int, _ body: String) {
        let response = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(body.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

@Suite(.serialized)
struct FRPEnrollmentTests {
    @MainActor private func session(_ host: String) async -> SessionState {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [EnrollmentProtocol.self]
        let session = SessionState(api: ServerAPI(configuration: configuration), vault: EnrollmentVault())
        await session.restore()
        await session.signIn(server: "https://" + host, username: "fixture", password: "fixture")
        return session
    }

    @Test @MainActor func wrongCurrentPasswordPreservesValidSessionButExpiredJWTDoesNot() async {
        let valid = await session("wrong-enrollment.test")
        let wrong = FRPEnrollmentState(session: valid, deviceID: 1, operation: .issue)
        await wrong.submit(password: "wrong")
        #expect(valid.account != nil)
        #expect(wrong.error == ServerError.reauthenticationRejected.localizedDescription)
        #expect(!wrong.submitted && wrong.receipt == nil)
        let expired = await session("expired-enrollment.test")
        let rejected = FRPEnrollmentState(session: expired, deviceID: 1, operation: .issue)
        await rejected.submit(password: "fixture")
        #expect(expired.account == nil)
        #expect(rejected.receipt == nil)
    }

    @Test @MainActor func misboundReceiptCannotBeExposedAnd204RevokeIsARealEmptyReceipt() async {
        let malformed = await session("malformed-enrollment.test")
        let issue = FRPEnrollmentState(session: malformed, deviceID: 1, operation: .issue)
        await issue.submit(password: "fixture")
        #expect(issue.receipt == nil && issue.submitted)
        #expect(malformed.account != nil)
        let valid = await session("enrollment.test")
        let revoke = FRPEnrollmentState(session: valid, deviceID: 1, operation: .revoke)
        await revoke.submit(password: "fixture")
        #expect(revoke.revoked && revoke.error == nil)
        #expect(valid.account != nil)
    }

    @Test @MainActor func closingAndSwitchingAccountsCannotRevealALateCode() async throws {
        let session = await session("held-enrollment.test")
        let state = FRPEnrollmentState(session: session, deviceID: 1, operation: .issue)
        let pending = Task { await state.submit(password: "fixture") }
        let deadline = ContinuousClock.now.advanced(by: .seconds(3))
        while !EnrollmentProtocol.gate.waiting {
            guard ContinuousClock.now < deadline else { throw ServerError.unavailable }
            try await Task.sleep(for: .milliseconds(5))
        }
        state.close()
        await session.signOut()
        await session.signIn(server: "https://other-enrollment.test", username: "fixture", password: "fixture")
        EnrollmentProtocol.gate.release()
        await pending.value
        #expect(state.receipt == nil && state.error == nil)
        #expect(session.account != nil)
        let fresh = FRPEnrollmentState(session: session, deviceID: 1, operation: .issue)
        await fresh.submit(password: "fixture")
        #expect(fresh.receipt != nil)
        fresh.expire()
        #expect(fresh.receipt == nil && fresh.submitted)
    }
}
