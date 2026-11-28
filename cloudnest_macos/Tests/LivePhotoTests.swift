import Foundation
import Testing
@testable import CloudNestCore

private final class LivePhotoVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ value: SavedSession) throws { saved = value }
    func clear() throws { saved = nil }
}
private final class LivePhotoGate: @unchecked Sendable {
    private let lock = NSLock()
    private var held: [String:LivePhotoProtocol] = [:]
    private var counts: [String:Int] = [:]
    func count(_ key: String) -> Int { lock.lock(); defer { lock.unlock() }; return counts[key,default:0] }
    func next(_ key: String) { lock.lock(); counts[key,default:0] += 1; lock.unlock() }
    func hold(_ object: LivePhotoProtocol, host: String) { lock.lock(); held[host] = object; lock.unlock() }
    func waiting(_ host: String) -> Bool { lock.lock(); defer { lock.unlock() }; return held[host] != nil }
    func release(_ host: String) {
        lock.lock(); let object = held.removeValue(forKey:host); lock.unlock()
        guard let object else { return }
        object.json(object.request.httpMethod == "DELETE" ? ["message":"unbound"] : ["item":LivePhotoProtocol.pair()])
    }
}
private final class LivePhotoProtocol: URLProtocol, @unchecked Sendable {
    static let gate = LivePhotoGate()
    static func resource(_ id: Int, filename: String) -> [String:Any] {
        ["id":id,"filename":filename,"size":42,"parent_id":0,"user_id":7,"file_type":(filename as NSString).pathExtension,"current_version":3]
    }
    static func pair() -> [String:Any] {
        ["id":5,"image_file_id":11,"video_file_id":12,"image_file":resource(11,filename:"Still.jpg"),"video_file":resource(12,filename:"Motion.mov")]
    }
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let host = request.url!.host!, path = request.url!.path
        if path == "/api/auth/login" { json(["token":UUID().uuidString,"token_type":"Bearer","user":["id":7,"username":"fixture","is_admin":false]]); return }
        if path == "/api/resource/health" || path == "/api/auth/logout" { json(["message":"ok"]); return }
        #expect(request.value(forHTTPHeaderField:"Authorization")?.hasPrefix("Bearer ") == true)
        Self.gate.next(host+":"+request.httpMethod!+":"+path)
        if path == "/api/files/search" {
            json(["media_kind":"live_photo_motion","items":[["id":12,"filename":"Motion.mov","size":42,"parent_id":0,"is_folder":0,"user_id":7,"file_type":"mov","current_version":3,"update_time":"2026-11-27T00:00:00Z"]],"pagination":["page":1,"limit":50,"total":1]]); return
        }
        if request.httpMethod != "GET" {
            if host.hasPrefix("late-") { Self.gate.hold(self,host:host); return }
            if host == "unknown.live.test" { respond(500,data:Data()); return }
            var row = Self.pair()
            if host == "misbound.live.test" { row["image_file_id"] = 99 }
            json(request.httpMethod == "DELETE" ? ["message":"unbound"] : ["item":row]); return
        }
        if host == "denied.live.test" { respond(403,data:Data()); return }
        var row = Self.pair()
        if host == "misbound.live.test" { row["image_file_id"] = 99 }
        if host == "legacy.live.test" {
            var image = Self.resource(11,filename:"Still.jpg"); image.removeValue(forKey:"current_version"); row["image_file"] = image
        }
        if path == "/api/live-photos" {
            let rows = host == "duplicate.live.test" ? [row,row] : [row]
            json(["items":rows,"pagination":["page":1,"limit":50,"total":rows.count]])
        } else { json(["item":row]) }
    }
    func json(_ object: [String:Any]) { respond(200,data:try! JSONSerialization.data(withJSONObject:object)) }
    func respond(_ status: Int, data: Data) {
        client?.urlProtocol(self,didReceive:HTTPURLResponse(url:request.url!,statusCode:status,httpVersion:nil,headerFields:nil)!,cacheStoragePolicy:.notAllowed)
        if !data.isEmpty { client?.urlProtocol(self,didLoad:data) }
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

@Suite(.serialized)
struct LivePhotoTests {
    private var image: FileItem { FileItem(id:11,filename:"Still.jpg",parentID:0,isFolder:0,size:42,currentVersion:3,updateTime:"2026-11-27T00:00:00Z",fileType:"jpg",userID:7) }
    private func api() -> ServerAPI {
        let config = URLSessionConfiguration.ephemeral; config.protocolClasses = [LivePhotoProtocol.self]
        return ServerAPI(configuration:config)
    }
    @MainActor private func session(_ host: String) async -> SessionState {
        let session = SessionState(api:api(),vault:LivePhotoVault())
        await session.restore(); await session.signIn(server:"https://"+host,username:"fixture",password:"fixture")
        return session
    }
    private func held(_ host: String) async throws {
        let deadline = ContinuousClock.now.advanced(by:.seconds(3))
        while !LivePhotoProtocol.gate.waiting(host) {
            guard ContinuousClock.now < deadline else { throw ServerError.unavailable }
            try await Task.sleep(for:.milliseconds(5))
        }
    }
    @Test func bindingAndPagesRequireBothResourcesAndRealVersions() async throws {
        for host in ["misbound.live.test","legacy.live.test","duplicate.live.test"] {
            do { _ = try await api().livePhotos(ServerOrigin("https://"+host),token:"synthetic",page:1); Issue.record("accepted invalid pair page") }
            catch { #expect(error as? ServerError == .invalidResponse) }
        }
        let pair = try await api().livePhoto(ServerOrigin("https://good.live.test"),token:"synthetic",id:5)
        #expect(pair.image.currentVersion == 3 && pair.video.currentVersion == 3)
        do { _ = try await api().bindLivePhoto(ServerOrigin("https://misbound.live.test"),token:"synthetic",imageID:11,videoID:12); Issue.record("accepted misbound mutation receipt") }
        catch { #expect(error as? ServerError == .invalidResponse) }
    }
    @Test @MainActor func unknownBindingCannotBeReplayedFromTheSameForm() async throws {
        let session = await session("unknown.live.test"), state = LivePhotoBindingState(session:session,image:image)
        await state.load(try PhotoQuery()); let video = try #require(state.page?.items.first)
        await state.bind(video); await state.bind(video)
        #expect(state.locked && state.receipt == nil && state.error != nil)
        #expect(LivePhotoProtocol.gate.count("unknown.live.test:POST:/api/live-photos/bind") == 1)
    }
    @Test @MainActor func discardedBindingDoesNotExposeALateReceipt() async throws {
        let host = "late-bind.live.test", session = await session(host)
        let state = LivePhotoBindingState(session:session,image:image)
        await state.load(try PhotoQuery()); let video = try #require(state.page?.items.first)
        let task = Task { await state.bind(video) }; try await held(host)
        state.discard(); LivePhotoProtocol.gate.release(host); await task.value
        #expect(state.receipt == nil && state.page == nil)
    }
    @Test @MainActor func discardedUnbindDoesNotReloadAnOffscreenList() async throws {
        let host = "late-unbind.live.test", session = await session(host)
        let state = LivePhotoState(session:session)
        await state.load(); let pair = try #require(state.page?.items.first)
        let task = Task { await state.unbind(pair) }; try await held(host)
        state.discard(); LivePhotoProtocol.gate.release(host); await task.value
        #expect(state.page == nil)
        #expect(LivePhotoProtocol.gate.count(host+":GET:/api/live-photos") == 1)
    }
}
