import Foundation
import CryptoKit
import Testing
@testable import CloudNestCore

private final class AuditTestVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ value: SavedSession) throws { saved = value }
    func clear() throws { saved = nil }
}
private final class AuditTestGate: @unchecked Sendable {
    private let lock = NSLock()
    private var held: [String:AuditProtocol] = [:]
    private var counts: [String:Int] = [:]
    private var stops: [String:Int] = [:]
    func hold(_ protocolObject: AuditProtocol, host: String) { lock.lock(); held[host] = protocolObject; lock.unlock() }
    func waiting(_ host: String) -> Bool { lock.lock(); defer { lock.unlock() }; return held[host] != nil }
    func next(_ host: String) -> Int { lock.lock(); defer { lock.unlock() }; counts[host,default:0] += 1; return counts[host]! }
    func stopped(_ host: String) { lock.lock(); stops[host,default:0] += 1; lock.unlock() }
    func stopCount(_ host: String) -> Int { lock.lock(); defer { lock.unlock() }; return stops[host,default:0] }
    func release(_ host: String, success: Bool) {
        lock.lock(); let object = held.removeValue(forKey:host); lock.unlock()
        if success { object?.respondCSV(finish:false) } else { object?.respond(401,data:Data()) }
    }
}
private final class AuditProtocol: URLProtocol, @unchecked Sendable {
    static let gate = AuditTestGate()
    static let header = "id,user_id,file_id,action,status,detail,client_ip,create_time\n"
    static let csv = Data((header+"10,7,,file_create,success,'=1+1,127.0.0.1,2026-11-27 00:00:00\n").utf8)
    private let lock = NSLock()
    private var stopped = false
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let host = request.url!.host!, path = request.url!.path
        if path == "/api/auth/login" {
            json(["token":UUID().uuidString,"token_type":"Bearer","user":["id":7,"username":"fixture","is_admin":true]]); return
        }
        if path == "/api/auth/me" { json(["user":["id":7,"username":"fixture","is_admin":false]]); return }
        if path == "/api/resource/health" || path == "/api/auth/logout" { json(["message":"ok"]); return }
        #expect(request.value(forHTTPHeaderField:"Authorization")?.hasPrefix("Bearer ") == true)
        if path.hasSuffix("/export") {
	        if host == "export-denied.audit.test" { respond(403,data:Data()); return }
            if host.hasPrefix("late-") { Self.gate.hold(self,host:host); return }
            respondCSV(finish:host != "slow.audit.test"); return
        }
        #expect(request.url!.query?.contains("cursor_mode=1") == true)
        if host == "clear.audit.test" && Self.gate.next(host) > 1 { respond(403,data:Data()); return }
        let items = URLComponents(url:request.url!,resolvingAgainstBaseURL:false)!.queryItems!
        func query(_ name: String) -> String? { items.first { $0.name == name }?.value }
        let all = query("all") == "1", id = query("before_id") == nil ? 10 : 9
        var row: [String:Any] = ["id":id,"user_id":all || host == "foreign.audit.test" ? 8 : 7,"file_id":NSNull(),"action":"file_create","status":"success","detail":"synthetic audit row","client_ip":"127.0.0.1","create_time":"2026-11-27T00:00:00Z"]
        if host == "badfilter.audit.test" { row["action"] = "other_action" }
        var rows = [row]
        if host == "duplicate.audit.test" { rows.append(row) }
        let before = host == "badcursor.audit.test" ? 8 : id == 10 ? 10 : 0
        json(["items":rows,"scope":host == "badscope.audit.test" ? "all" : all ? "all" : "self","user_id":7,
            "pagination":["limit":50,"total":2],"cursor":["snapshot_id":10,"next_before_id":before,"next_after_id":id == 9 ? 9 : 0]])
    }
    func respondCSV(finish: Bool) {
        let host = request.url!.host!, slow = host == "slow.audit.test"
        let data = slow ? Data((Self.header+String(repeating:"10,7,,file_create,success,row,127.0.0.1,2026-11-27 00:00:00\n",count:1000)).utf8) : Self.csv
        let hash = SHA256.hash(data:data)
        let snapshot = URLComponents(url:request.url!,resolvingAgainstBaseURL:false)!.queryItems!.first { $0.name == "snapshot_id" }!.value!
        var headers = ["Content-Type":"text/csv; charset=utf-8","Content-Length":String(data.count),"ETag":"\"sha256-\(TransferDisk.hex(hash))\"","Repr-Digest":"sha-256=:\(Data(hash).base64EncodedString()):",
            "X-CloudNest-Audit-Scope":"self","X-CloudNest-Audit-User":"7","X-CloudNest-Audit-Snapshot":snapshot,
            "X-CloudNest-Audit-Rows":slow ? "1000" : "1","X-CloudNest-Audit-Truncated":"false","X-CloudNest-Audit-Timezone":"UTC"]
        if host == "badreceipt.audit.test" { headers["X-CloudNest-Audit-User"] = "8" }
        var body = finish ? data : Data(data.prefix(slow ? 4096 : max(1,data.count/2)))
        if host == "corrupt.audit.test" { body[0] ^= 0xff }
        respond(200,data:body,headers:headers,finish:finish)
    }
    private func json(_ object: [String:Any]) { respond(200,data:try! JSONSerialization.data(withJSONObject:object)) }
    func respond(_ status: Int, data: Data, headers: [String:String] = [:], finish: Bool = true) {
        lock.lock(); let cancelled = stopped; lock.unlock()
        guard !cancelled else { return }
        client?.urlProtocol(self,didReceive:HTTPURLResponse(url:request.url!,statusCode:status,httpVersion:nil,headerFields:headers)!,cacheStoragePolicy:.notAllowed)
        if !data.isEmpty { client?.urlProtocol(self,didLoad:data) }
        if finish { client?.urlProtocolDidFinishLoading(self) }
    }
    override func stopLoading() { lock.lock(); stopped = true; lock.unlock(); Self.gate.stopped(request.url!.host!) }
}

@Suite(.serialized)
struct AuditTests {
    private func api() -> ServerAPI {
        let configuration = URLSessionConfiguration.ephemeral; configuration.protocolClasses = [AuditProtocol.self]
        return ServerAPI(configuration:configuration)
    }
    @MainActor private func makeSession(_ host: String) async -> SessionState {
        let session = SessionState(api:api(),vault:AuditTestVault())
        await session.restore(); await session.signIn(server:"https://"+host,username:"fixture",password:"fixture")
        return session
    }
    private func directory() throws -> URL {
        let url = FileManager.default.temporaryDirectory.appending(path:"cloudnest-audit-unit-\(UUID())")
        try FileManager.default.createDirectory(at:url,withIntermediateDirectories:true,attributes:[.posixPermissions:0o700]); return url
    }
    private func waitForGate(_ host: String) async throws {
        let deadline = ContinuousClock.now.advanced(by:.seconds(3))
        while !AuditProtocol.gate.waiting(host) { guard ContinuousClock.now < deadline else { throw ServerError.unavailable }; try await Task.sleep(for:.milliseconds(5)) }
    }

    @Test func queriesAndPagesRejectUnboundContent() async throws {
        #expect(throws:ServerError.invalidOperation) { try AuditQuery(action:"bad\nname") }
        #expect(throws:ServerError.invalidOperation) { try AuditQuery(start:Date(timeIntervalSince1970:2),end:Date(timeIntervalSince1970:1)) }
        let query = try AuditQuery(start:Date(timeIntervalSince1970:1.9),end:Date(timeIntervalSince1970:2.9))
        #expect(query.start?.timeIntervalSince1970 == 1 && query.end?.timeIntervalSince1970 == 2)
        for host in ["foreign.audit.test","duplicate.audit.test","badcursor.audit.test","badscope.audit.test","badfilter.audit.test"] {
            await #expect(throws:ServerError.invalidResponse) { try await api().auditPage(ServerOrigin("https://"+host),token:"fixture",userID:7,query:AuditQuery(action:"file_create")) }
        }
    }

    @Test @MainActor func pagingKeepsTheRangeAndPermissionErrorsClearOldRows() async throws {
        let session = await makeSession("paging.audit.test")
        let state = AuditState(session:session)
        await state.load(try AuditQuery()); #expect(state.page?.items.first?.id == 10)
        await state.next(); #expect(state.page?.items.first?.id == 9 && state.page?.snapshotID == 10)
        await state.previous(); #expect(state.page?.items.first?.id == 10 && state.page?.snapshotID == 10)
        state.discard()
        let denied = await self.makeSession("clear.audit.test")
        let clearing = AuditState(session:denied)
        await clearing.load(try AuditQuery()); #expect(clearing.page != nil)
        await clearing.load(try AuditQuery()); #expect(clearing.page == nil && clearing.error == ServerError.forbidden.localizedDescription && denied.account != nil)
        clearing.discard()
        let admin = AuditState(session:session)
        await admin.load(try AuditQuery(all:true)); #expect(admin.page?.items.first?.userID == 8)
        await session.refresh()
        await admin.load(try AuditQuery(all:true)); #expect(admin.page == nil && admin.error == ServerError.forbidden.localizedDescription && session.account != nil)
        admin.discard()
    }

    @Test @MainActor func verifiedCSVPreservesExistingFilesAndRejectsCorruption() async throws {
        let root = try directory(); defer { try? FileManager.default.removeItem(at:root) }
        let destination = root.appending(path:"Audit.csv"); try Data("keep".utf8).write(to:destination)
        let good = AuditState(session:await makeSession("good.audit.test"))
        await good.load(try AuditQuery()); await good.export(try #require(good.exportSelection),to:destination)
        let receipt = try #require(good.receipt)
        #expect(receipt.url.lastPathComponent == "Audit (1).csv" && receipt.rows == 1 && !receipt.truncated && !receipt.cleanupIncomplete)
        #expect(try Data(contentsOf:receipt.url) == AuditProtocol.csv)
        #expect(try Data(contentsOf:destination) == Data("keep".utf8))
        #expect(good.received > 0)
        await good.load(try AuditQuery())
        #expect(good.received == 0 && good.totalBytes == 0 && !good.verifying)
        for host in ["corrupt.audit.test","badreceipt.audit.test","export-denied.audit.test"] {
            let account = await makeSession(host)
            let state = AuditState(session:account); await state.load(try AuditQuery())
            await state.export(try #require(state.exportSelection),to:root.appending(path:host+".csv"))
            #expect(state.receipt == nil && state.error != nil)
            if host == "export-denied.audit.test" { #expect(state.page == nil && account.account != nil) }
            #expect(!FileManager.default.fileExists(atPath:root.appending(path:host+".csv").path))
            state.discard()
        }
        #expect(try !FileManager.default.contentsOfDirectory(atPath:root.path).contains { $0.hasPrefix(".cloudnest-") })
        good.discard()
    }

    @Test @MainActor func cancellingCSVWaitsForCleanup() async throws {
        let root = try directory(); defer { try? FileManager.default.removeItem(at:root) }
        let state = AuditState(session:await makeSession("slow.audit.test")); await state.load(try AuditQuery())
        let selection = try #require(state.exportSelection)
        let job = Task { await state.export(selection,to:root.appending(path:"Cancelled.csv")) }
        let deadline = ContinuousClock.now.advanced(by:.seconds(3))
        while state.received == 0 { guard ContinuousClock.now < deadline else { state.cancel(); throw ServerError.unavailable }; try await Task.sleep(for:.milliseconds(5)) }
        state.cancel()
        let stopped = ContinuousClock.now.advanced(by:.seconds(3))
        while state.busy { guard ContinuousClock.now < stopped else { throw ServerError.unavailable }; try await Task.sleep(for:.milliseconds(5)) }
        await job.value
        #expect(state.receipt == nil && state.error?.contains("取消") == true)
        #expect(try FileManager.default.contentsOfDirectory(atPath:root.path).isEmpty)
        state.discard()
    }

    @Test @MainActor func oldExportUnauthorizedCannotRemoveANewSession() async throws {
        let host = "late-unauthorized.audit.test", session = await makeSession(host)
        let state = AuditState(session:session); await state.load(try AuditQuery())
        let selection = try #require(state.exportSelection), identity = try #require(session.transferIdentity)
        let pending = Task { try await session.auditExportResponse(selection:selection,expectedIdentity:identity) }
        try await waitForGate(host)
        await session.signOut(); await session.signIn(server:"https://fresh.audit.test",username:"fixture",password:"fixture")
        AuditProtocol.gate.release(host,success:false)
        do { _ = try await pending.value; Issue.record("Old audit export was returned to a new session") }
        catch { #expect(error is CancellationError) }
        #expect(session.transferIdentity?.server == "https://fresh.audit.test")
        state.discard()
    }

    @Test @MainActor func oldSuccessfulStreamIsCancelledBeforeReturningToANewSession() async throws {
        let host = "late-success.audit.test", session = await makeSession(host)
        let state = AuditState(session:session); await state.load(try AuditQuery())
        let selection = try #require(state.exportSelection), identity = try #require(session.transferIdentity)
        let pending = Task { try await session.auditExportResponse(selection:selection,expectedIdentity:identity) }
        try await waitForGate(host)
        await session.signOut(); await session.signIn(server:"https://new.audit.test",username:"fixture",password:"fixture")
        AuditProtocol.gate.release(host,success:true)
        do { _ = try await pending.value; Issue.record("Unowned audit body remained open") }
        catch { #expect(error is CancellationError) }
        let deadline = ContinuousClock.now.advanced(by:.seconds(3))
        while AuditProtocol.gate.stopCount(host) == 0 { guard ContinuousClock.now < deadline else { throw ServerError.unavailable }; try await Task.sleep(for:.milliseconds(5)) }
        #expect(session.transferIdentity?.server == "https://new.audit.test")
        state.discard()
    }
}
