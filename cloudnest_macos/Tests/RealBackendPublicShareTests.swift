import Foundation
import CryptoKit
import Testing
@testable import CloudNestCore

private struct PublicBackendFixture: Decodable {
    let origin: String; let certificate: String; let username: String; let password: String; let passcode: String
    let code: String; let slow_code: String; let corrupt_code: String; let empty_code: String; let folder_code: String; let expired_code: String
    let source_size: Int64; let source_sha256: String; let directory: String
}
private final class PublicBackendVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ value: SavedSession) throws { saved = value }
    func clear() throws { saved = nil }
}

extension RealBackendTests {
    @Test(.enabled(if: ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_PUBLIC_SHARE_FIXTURE"] != nil))
    @MainActor func realBackendPublicShareReceiver() async throws {
        let path = try #require(ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_PUBLIC_SHARE_FIXTURE"])
        let f = try JSONDecoder().decode(PublicBackendFixture.self, from: Data(contentsOf: URL(fileURLWithPath: path)))
        let delegate = try FixtureTLSDelegate(der: Data(contentsOf: URL(fileURLWithPath: f.certificate)))
        let configuration = URLSessionConfiguration.ephemeral
        configuration.httpAdditionalHeaders = ["Authorization":"Bearer forbidden-fixture", "Cookie":"private=fixture", "Proxy-Authorization":"private-fixture"]
        let transport = URLSession(configuration: configuration, delegate: delegate, delegateQueue: nil)
        defer { transport.invalidateAndCancel() }
        let api = ServerAPI(session: transport, delegate: delegate), session = SessionState(api: ServerAPI(session: transport, delegate: delegate), vault: PublicBackendVault())
        await session.restore(); await session.signIn(server: f.origin, username: f.username, password: f.password)
        #expect(session.account != nil)
        func target(_ code: String) throws -> PublicShareTarget { try PublicShareTarget(f.origin+"/api/public/shares/"+code) }
        let client = PublicShareClient(api: api)
        do { _ = try await client.metadata(target("Unauthorized1"), passcode:f.passcode); Issue.record("Anonymous 401 accepted") }
        catch { #expect(error as? PublicShareError == .notFound && session.account != nil) }
        do { _ = try await client.metadata(target("Redirect1234"), passcode:f.passcode); Issue.record("Anonymous redirect accepted") }
        catch { #expect(error as? PublicShareError == .invalidResponse) }
        await session.signOut()
        #expect(session.account == nil && !session.hasSavedSession)
        for value in ["", "wrong-passcode"] {
            do { _ = try await client.metadata(target(f.code), passcode:value); Issue.record("Protected metadata accepted wrong passcode") }
            catch { #expect(error as? PublicShareError == .passcodeRejected) }
        }
        do { _ = try await client.metadata(target(f.expired_code), passcode:f.passcode); Issue.record("Expired link accepted") }
        catch { #expect(error as? PublicShareError == .expiredOrExhausted) }
        let directory = URL(fileURLWithPath:f.directory)
        let metadata = try await client.metadata(target(f.code), passcode:f.passcode)
        #expect(metadata.size == f.source_size && metadata.maxDownloads == 1)
        let receipt = try await PublicShareDownloader().download(client:client, metadata:metadata, passcode:f.passcode, destination:directory.appending(path:"Large.bin")) { _,_,_ in }
        #expect(receipt.bytes == f.source_size && receipt.sha256 == f.source_sha256 && !receipt.cleanupIncomplete)
        #expect(try TransferDisk.hex(SHA256.hash(data:Data(contentsOf:receipt.url))) == f.source_sha256)
        do {
            _ = try await PublicShareDownloader().download(client:client, metadata:metadata, passcode:f.passcode, destination:directory.appending(path:"Exhausted.bin")) { _,_,_ in }
            Issue.record("Native anonymous download exceeded its quota")
        } catch { #expect((error as? PublicShareTransferFailure)?.cause as? PublicShareError == .expiredOrExhausted) }
        let corrupt = try await client.metadata(target(f.corrupt_code), passcode:f.passcode)
        do {
            _ = try await PublicShareDownloader().download(client:client, metadata:corrupt, passcode:f.passcode, destination:directory.appending(path:"Corrupt.bin")) { _,_,_ in }
            Issue.record("Real wire corruption was published")
        } catch { #expect((error as? PublicShareTransferFailure)?.cause as? TransferError == .integrity) }
        for (code,name) in [(f.empty_code,"Empty.txt"),(f.folder_code,"Folder.zip")] {
            let item = try await client.metadata(target(code), passcode:f.passcode)
            let result = try await PublicShareDownloader().download(client:client, metadata:item, passcode:f.passcode, destination:directory.appending(path:name)) { _,_,_ in }
            #expect(!result.cleanupIncomplete)
            let saved = try Data(contentsOf:result.url)
            if name == "Empty.txt" { #expect(result.bytes == 0 && saved.isEmpty) }
            else { #expect(result.bytes >= 22 && saved.prefix(2) == Data("PK".utf8)) }
        }
        let state = PublicShareState(client:client)
        await state.load(target:try target(f.slow_code), passcode:f.passcode)
        let slow = try #require(state.metadata)
        let job = Task { await state.download(slow,to:directory.appending(path:"Cancelled.bin")) }
        let deadline = ContinuousClock.now.advanced(by:.seconds(8))
        while state.received == 0 {
            guard ContinuousClock.now < deadline else { state.cancel(); await job.value; throw PublicShareError.unavailable }
            try await Task.sleep(for:.milliseconds(10))
        }
        state.cancel()
        let cancellationDeadline = ContinuousClock.now.advanced(by:.seconds(3))
        while state.busy {
            guard ContinuousClock.now < cancellationDeadline else { throw PublicShareError.unavailable }
            try await Task.sleep(for:.milliseconds(10))
        }
        await job.value
        #expect(state.receipt == nil && !state.busy && state.error?.contains("取消") == true && session.account == nil)
        let files = try FileManager.default.contentsOfDirectory(atPath:directory.path)
        #expect(!files.contains { $0.hasPrefix(".cloudnest-") || ["Corrupt.bin","Exhausted.bin","Cancelled.bin"].contains($0) })
        state.clear()
    }
}
