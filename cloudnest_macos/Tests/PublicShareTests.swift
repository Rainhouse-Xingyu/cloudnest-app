import Foundation
import CryptoKit
import Testing
@testable import CloudNestCore

private final class PublicTestVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ value: SavedSession) throws { saved = value }
    func clear() throws { saved = nil }
}
private final class PublicGate: @unchecked Sendable {
    private let lock = NSLock()
    private var counts: [String: Int] = [:]
    private var held: PublicProtocol?
    var waiting: Bool { lock.lock(); defer { lock.unlock() }; return held != nil }
    func next(_ key: String) -> Int { lock.lock(); defer { lock.unlock() }; counts[key, default: 0] += 1; return counts[key]! }
    func hold(_ value: PublicProtocol) { lock.lock(); held = value; lock.unlock() }
    func release() { lock.lock(); let value = held; held = nil; lock.unlock(); value?.respond(401, data: Data()) }
}
private final class PublicProtocol: URLProtocol, @unchecked Sendable {
    static let payload = Data(repeating: 0x4e, count: 96 * 1024 + 17)
    static let gate = PublicGate()
    private let lock = NSLock()
    private var stopped = false
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let path = request.url?.path ?? "", host = request.url?.host ?? ""
        if path == "/api/auth/login" {
            respond(200, data: Data(#"{"token":"fixture-account","token_type":"Bearer","user":{"id":17,"username":"fixture","is_admin":false}}"#.utf8)); return
        }
        if path == "/api/resource/health" || path == "/api/auth/logout" { respond(200, data: Data(#"{"message":"ok"}"#.utf8)); return }
        #expect(request.value(forHTTPHeaderField: "Authorization") == nil)
        #expect(request.value(forHTTPHeaderField: "Cookie") == nil)
        #expect(request.value(forHTTPHeaderField: "Proxy-Authorization") == nil)
        #expect(request.url?.query == nil && request.value(forHTTPHeaderField: "Range") == nil)
        #expect(request.value(forHTTPHeaderField: "X-Share-Passcode") == "fixture-code")
        if host == "unauthorized.public.test" { respond(401, data: Data()); return }
        if host == "redirect.public.test" { respond(302, data: Data(), headers: ["Location": "https://other.test/capture"]); return }
        if path.hasSuffix("/download") {
            let data = Self.payload
            let hash = SHA256.hash(data: data)
            let expected = host == "slow.public.test" ? data.count * 2 : data.count
            var headers = ["Content-Length": String(expected), "ETag": "\"sha256-\(TransferDisk.hex(hash))\"", "Repr-Digest": "sha-256=:\(Data(hash).base64EncodedString()):"]
            if host == "badheader.public.test" { headers["Repr-Digest"] = "sha-256=:\(Data(repeating: 0, count: 32).base64EncodedString()):" }
            var body = data
            if host == "badbody.public.test" { body[0] ^= 0xff }
            respond(200, data: body, headers: headers, finish: host != "slow.public.test")
            return
        }
        if host == "late.public.test" && Self.gate.next(host) == 1 { Self.gate.hold(self); return }
        if host == "oversized.public.test" { respond(200, data: Data(repeating: 0x20, count: 65537)); return }
        let code = host == "misbound.public.test" ? "Other1234567" : "Abcdef123456"
        let size = host == "slow.public.test" ? Self.payload.count * 2 : Self.payload.count
        let json = "{\"share_code\":\"\(code)\",\"expire_time\":null,\"requires_passcode\":true,\"max_downloads\":0,\"download_count\":0,\"file\":{\"id\":8,\"filename\":\"Received.txt\",\"is_folder\":0,\"size\":\(size)}}"
        respond(200, data: Data(json.utf8))
    }
    func respond(_ status: Int, data: Data, headers: [String: String] = [:], finish: Bool = true) {
        lock.lock(); let cancelled = stopped; lock.unlock()
        guard !cancelled else { return }
        let response = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: nil, headerFields: headers)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        if !data.isEmpty { client?.urlProtocol(self, didLoad: data) }
        if finish { client?.urlProtocolDidFinishLoading(self) }
    }
    override func stopLoading() { lock.lock(); stopped = true; lock.unlock() }
}

@Suite(.serialized)
struct PublicShareTests {
    private func api() -> ServerAPI {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [PublicProtocol.self]
        configuration.httpAdditionalHeaders = ["Authorization": "Bearer should-not-be-sent", "Cookie": "private=fixture", "Proxy-Authorization": "private-fixture"]
        return ServerAPI(configuration: configuration)
    }
    private func target(_ host: String) throws -> PublicShareTarget { try PublicShareTarget("https://\(host)/api/public/shares/Abcdef123456") }
    private func directory() throws -> URL {
        let url = FileManager.default.temporaryDirectory.appending(path: "cloudnest-public-unit-\(UUID())")
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        return url
    }

    @Test func linkParserRejectsCredentialAndPathConfusion() throws {
        let valid = try PublicShareTarget("  https://SHARE.test:443/api/public/shares/Abcdef123456 \n")
        #expect(valid.origin.url.absoluteString == "https://share.test" && valid.code == "Abcdef123456")
        for link in ["http://share.test/api/public/shares/Abcdef123456", "https://u:p@share.test/api/public/shares/Abcdef123456", "https://share.test/api/public/shares/a?passcode=secret", "https://share.test/api/public/shares/a#secret", "https://share.test/api/public/shares/%41bcdef123456", "https://share.test/api/public/shares/a/other", "https://share.test:0/api/public/shares/a"] {
            do { _ = try PublicShareTarget(link); Issue.record("Unsafe anonymous share target accepted") }
            catch { #expect(error as? PublicShareError == .invalidLink) }
        }
    }

    @Test @MainActor func anonymous401KeepsTheAccountAndBadMetadataFailsClosed() async throws {
        let api = api()
        let session = SessionState(api: api, vault: PublicTestVault())
        await session.restore(); await session.signIn(server: "https://unauthorized.public.test", username: "fixture", password: "fixture")
        #expect(session.account != nil)
        let state = PublicShareState(client: session.publicShareClient)
        await state.load(target: try target("unauthorized.public.test"), passcode: "fixture-code")
        #expect(state.metadata == nil && state.error == PublicShareError.notFound.localizedDescription && session.account != nil)
        for host in ["misbound.public.test", "oversized.public.test", "redirect.public.test"] {
            do { _ = try await PublicShareClient(api: api).metadata(target(host), passcode: "fixture-code"); Issue.record("Invalid anonymous metadata accepted") }
            catch { #expect(error as? PublicShareError == .invalidResponse) }
        }
        state.clear()
    }

    @Test @MainActor func replacementCannotPublishOldAnonymousUnauthorized() async throws {
        let state = PublicShareState(client: PublicShareClient(api: api())), link = try target("late.public.test")
        let first = Task { await state.load(target: link, passcode: "fixture-code") }
        let deadline = ContinuousClock.now.advanced(by: .seconds(3))
        while !PublicProtocol.gate.waiting {
            guard ContinuousClock.now < deadline else { throw PublicShareError.unavailable }
            try await Task.sleep(for: .milliseconds(5))
        }
        await state.load(target: link, passcode: "fixture-code")
        PublicProtocol.gate.release(); await first.value
        #expect(state.metadata?.fileID == 8 && state.error == nil && !state.busy)
        state.clear()
    }

    @Test func verifiedAnonymousDownloadPreservesExistingDestination() async throws {
        let client = PublicShareClient(api: api()), directory = try directory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let metadata = try await client.metadata(target("good.public.test"), passcode: "fixture-code")
        let destination = directory.appending(path: "Received.txt")
        try Data("existing".utf8).write(to: destination)
        let receipt = try await PublicShareDownloader().download(client: client, metadata: metadata, passcode: "fixture-code", destination: destination) { _, _, _ in }
        #expect(receipt.url.lastPathComponent == "Received (1).txt" && !receipt.cleanupIncomplete)
        #expect(try Data(contentsOf: destination) == Data("existing".utf8))
        #expect(try Data(contentsOf: receipt.url) == PublicProtocol.payload)
        #expect(try FileManager.default.contentsOfDirectory(atPath: directory.path).sorted() == ["Received (1).txt", "Received.txt"])
    }

    @Test func badIdentityOrBodyCannotPublishAFile() async throws {
        let client = PublicShareClient(api: api())
        for host in ["badheader.public.test", "badbody.public.test"] {
            let directory = try directory()
            defer { try? FileManager.default.removeItem(at: directory) }
            let metadata = try await client.metadata(target(host), passcode: "fixture-code")
            do {
                _ = try await PublicShareDownloader().download(client: client, metadata: metadata, passcode: "fixture-code", destination: directory.appending(path: "Denied.txt")) { _, _, _ in }
                Issue.record("Unverified anonymous bytes were published")
            } catch { #expect(error is PublicShareTransferFailure) }
            #expect(try FileManager.default.contentsOfDirectory(atPath: directory.path).isEmpty)
        }
    }

    @Test func anonymousDownloadsCannotWriteIntoRetainedLegacyPreviews() async throws {
        let client = PublicShareClient(api: api()), directory = try directory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let legacy = LegacyPreviewDirectory(store: directory)
        try FileManager.default.createDirectory(at: legacy.root, withIntermediateDirectories: true)
        let old = legacy.root.appending(path: "Retained.txt")
        try Data("retained bytes".utf8).write(to: old)
        let metadata = try await client.metadata(target("legacy.public.test"), passcode: "fixture-code")
        do {
            _ = try await PublicShareDownloader(legacyPreviews: legacy).download(client: client, metadata: metadata, passcode: "fixture-code", destination: old) { _, _, _ in }
            Issue.record("Anonymous download wrote into retained preview namespace")
        } catch { #expect(error as? TransferError == .legacyPreview) }
        #expect(try Data(contentsOf: old) == Data("retained bytes".utf8))
        #expect(try FileManager.default.contentsOfDirectory(atPath: legacy.root.path) == ["Retained.txt"])
    }

    @Test @MainActor func cancellingAnActiveAnonymousBodyRemovesThePart() async throws {
        let directory = try directory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let state = PublicShareState(client: PublicShareClient(api: api()))
        await state.load(target: try target("slow.public.test"), passcode: "fixture-code")
        let metadata = try #require(state.metadata)
        let job = Task { await state.download(metadata, to: directory.appending(path: "Cancelled.txt")) }
        let deadline = ContinuousClock.now.advanced(by: .seconds(3))
        while state.received == 0 {
            guard ContinuousClock.now < deadline else { throw PublicShareError.unavailable }
            try await Task.sleep(for: .milliseconds(5))
        }
        state.cancel()
        let cancellationDeadline = ContinuousClock.now.advanced(by: .seconds(3))
        while state.busy {
            guard ContinuousClock.now < cancellationDeadline else { throw PublicShareError.unavailable }
            try await Task.sleep(for: .milliseconds(5))
        }
        await job.value
        #expect(!state.busy && state.receipt == nil && state.error?.contains("取消") == true)
        #expect(try FileManager.default.contentsOfDirectory(atPath: directory.path).isEmpty)
        state.clear()
    }
}
