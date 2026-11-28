import Foundation
import CryptoKit
import Testing
@testable import CloudNestCore

private final class PhotoVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ value: SavedSession) throws { saved = value }
    func clear() throws { saved = nil }
}
private final class PhotoGate: @unchecked Sendable {
    private let lock = NSLock()
    private var requests: [String:Int] = [:]
    private var stopped: [String:Int] = [:]
    func start(_ host: String) { lock.lock(); requests[host,default:0] += 1; lock.unlock() }
    func stop(_ host: String) { lock.lock(); stopped[host,default:0] += 1; lock.unlock() }
    func counts(_ host: String) -> (Int,Int) { lock.lock(); defer { lock.unlock() }; return (requests[host,default:0],stopped[host,default:0]) }
}
private final class PhotoProtocol: URLProtocol, @unchecked Sendable {
    static let gate = PhotoGate()
    static let thumbnail = Data("synthetic encoded-thumbnail protocol fixture".utf8)
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let host = request.url!.host!, path = request.url!.path
        if path == "/api/auth/login" {
            json(["token":UUID().uuidString,"token_type":"Bearer","user":["id":7,"username":"photo-fixture","is_admin":false]]); return
        }
        if path == "/api/resource/health" || path == "/api/auth/logout" { json(["message":"ok"]); return }
        #expect(request.value(forHTTPHeaderField:"Authorization")?.hasPrefix("Bearer ") == true)
        if path.hasSuffix("/thumbnail") {
            Self.gate.start(host)
            if host == "bounded.photo.test" { return }
            let hash = SHA256.hash(data:Self.thumbnail)
            var body = Self.thumbnail
            if host == "corrupt.photo.test" { body[0] ^= 0xff }
            var headers = ["Content-Type":"image/jpeg","Content-Length":String(body.count),"ETag":"\"sha256-\(TransferDisk.hex(hash))\"","Repr-Digest":"sha-256=:\(Data(hash).base64EncodedString()):"]
            if host == "oversize.photo.test" { headers["Content-Length"] = String(512*1024+1) }
            respond(200,data:body,headers:headers); return
        }
        let items = URLComponents(url:request.url!,resolvingAgainstBaseURL:false)!.queryItems!
        func value(_ name: String) -> String? { items.first { $0.name == name }?.value }
        #expect(value("media_kind") == "image")
        if value("name") == "denied" { respond(403,data:Data()); return }
        let page = Int(value("page")!)!
        var rows: [[String:Any]] = (1...6).map { ["id":$0,"filename":"Photo \($0).png","parent_id":0,"is_folder":0,"size":40,"current_version":1,"update_time":"2026-11-27T00:00:00Z","file_type":"png","user_id":7] }
        if value("name") == "empty" { rows = [] }
        if host == "duplicate.photo.test" { rows[1] = rows[0] }
        if host == "folder.photo.test" { rows[0]["is_folder"] = 1 }
        if host == "document.photo.test" { rows[0]["filename"] = "Private.txt" }
        json(["items":rows,"media_kind":host == "legacy.photo.test" ? "" : "image","pagination":["page":page,"limit":50,"total":rows.count]])
    }
    func json(_ object: [String:Any]) { respond(200,data:try! JSONSerialization.data(withJSONObject:object)) }
    func respond(_ status: Int, data: Data, headers: [String:String] = [:]) {
        client?.urlProtocol(self,didReceive:HTTPURLResponse(url:request.url!,statusCode:status,httpVersion:nil,headerFields:headers)!,cacheStoragePolicy:.notAllowed)
        if !data.isEmpty { client?.urlProtocol(self,didLoad:data) }
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() { if request.url!.path.hasSuffix("/thumbnail") { Self.gate.stop(request.url!.host!) } }
}

@Suite(.serialized)
struct PhotoTests {
    private func api() -> ServerAPI {
        let config = URLSessionConfiguration.ephemeral; config.protocolClasses = [PhotoProtocol.self]
        return ServerAPI(configuration:config)
    }
    @MainActor private func session(_ host: String) async -> SessionState {
        let state = SessionState(api:api(),vault:PhotoVault())
        await state.restore(); await state.signIn(server:"https://"+host,username:"fixture",password:"fixture")
        return state
    }
    @Test func pagesRequireMediaCapabilityAndValidResources() async throws {
        #expect(throws:ServerError.invalidOperation) { try PhotoQuery(name:"bad\nname") }
        #expect(throws:ServerError.invalidOperation) { try PhotoQuery(name:String(repeating:"x",count:201)) }
        for host in ["legacy.photo.test","duplicate.photo.test","folder.photo.test","document.photo.test"] {
            do { _ = try await api().photos(ServerOrigin("https://"+host),token:"synthetic",query:PhotoQuery(),page:1); Issue.record("accepted invalid media page") }
            catch { #expect(error as? ServerError == .invalidResponse) }
        }
        let page = try await api().photos(ServerOrigin("https://good.photo.test"),token:"synthetic",query:PhotoQuery(),page:1)
        #expect(page.items.count == 6 && !page.hasNext)
    }
    @Test func thumbnailIntegrityAndSizeAreCheckedBeforeUse() async throws {
        let api = api()
        #expect(try await api.photoThumbnail(ServerOrigin("https://good.photo.test"),token:"synthetic",fileID:1) == PhotoProtocol.thumbnail)
        do { _ = try await api.photoThumbnail(ServerOrigin("https://corrupt.photo.test"),token:"synthetic",fileID:1); Issue.record("accepted corrupt thumbnail") }
        catch { #expect(error as? TransferError == .integrity) }
        do { _ = try await api.photoThumbnail(ServerOrigin("https://oversize.photo.test"),token:"synthetic",fileID:1); Issue.record("accepted oversized thumbnail") }
        catch { #expect(error as? ServerError == .invalidResponse) }
    }
    @Test @MainActor func replacementCancelsFourRequestsWithoutStartingTheRest() async throws {
        let session = await session("bounded.photo.test"), state = PhotoState(session:session)
        let first = Task { await state.load(try! PhotoQuery()) }
        let deadline = ContinuousClock.now.advanced(by:.seconds(3))
        while PhotoProtocol.gate.counts("bounded.photo.test").0 < 4 {
            guard ContinuousClock.now < deadline else { Issue.record("four thumbnail requests did not start"); state.discard(); await first.value; return }
            try await Task.sleep(for:.milliseconds(5))
        }
        #expect(state.page?.items.count == 6 && PhotoProtocol.gate.counts("bounded.photo.test").0 == 4)
        await state.load(try PhotoQuery(name:"empty")); await first.value
        let cleanup = ContinuousClock.now.advanced(by:.seconds(3))
        while PhotoProtocol.gate.counts("bounded.photo.test").1 < 4 {
            guard ContinuousClock.now < cleanup else { Issue.record("thumbnail requests retained after replacement"); return }
            try await Task.sleep(for:.milliseconds(5))
        }
        #expect(PhotoProtocol.gate.counts("bounded.photo.test").0 == 4)
        #expect(state.page?.items.isEmpty == true && state.thumbnails.isEmpty && state.thumbnailErrors.isEmpty)
    }
    @Test @MainActor func deniedRefreshRemovesOldMediaAndKeepsTheAccount() async throws {
        let session = await session("clear.photo.test"), state = PhotoState(session:session)
        await state.load(try PhotoQuery())
        #expect(state.thumbnails.count == 6)
        await state.load(try PhotoQuery(name:"denied"))
        #expect(state.page == nil && state.thumbnails.isEmpty && state.error != nil && session.account != nil)
    }
}
