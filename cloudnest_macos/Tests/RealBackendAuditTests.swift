import Foundation
import CryptoKit
import Testing
@testable import CloudNestCore

private struct NativeAuditFixture: Decodable {
    let origin: String; let certificate: String; let username: String; let password: String
    let role: String; let action: String; let corrupt_action: String; let slow_action: String; let date: String
    let user_id: Int64; let owner_id: Int64; let peer_id: Int64; let directory: String
}
private final class NativeAuditVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ value: SavedSession) throws { saved = value }
    func clear() throws { saved = nil }
}

extension RealBackendTests {
    @Test(.enabled(if:ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_AUDIT_FIXTURE"] != nil))
    @MainActor func realBackendAudit() async throws {
        let path = try #require(ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_AUDIT_FIXTURE"])
        let f = try JSONDecoder().decode(NativeAuditFixture.self,from:Data(contentsOf:URL(fileURLWithPath:path)))
        let delegate = try FixtureTLSDelegate(der:Data(contentsOf:URL(fileURLWithPath:f.certificate)))
        let transport = URLSession(configuration:.ephemeral,delegate:delegate,delegateQueue:nil)
        defer { transport.invalidateAndCancel() }
        let api = ServerAPI(session:transport,delegate:delegate), vault = NativeAuditVault()
        let session = SessionState(api:api,vault:vault)
        await session.restore(); await session.signIn(server:f.origin,username:f.username,password:f.password)
        let identity = try #require(session.transferIdentity), origin = try ServerOrigin(f.origin)
        #expect(identity.userID == f.user_id)
        let state = AuditState(session:session), query = try AuditQuery(action:f.action)
        await state.load(query)
        if f.role == "restricted" {
            #expect(state.page == nil && state.error == ServerError.forbidden.localizedDescription && session.account != nil)
            let selection = AuditExportSelection(query:query,snapshotID:1,total:1)
            await #expect(throws:ServerError.forbidden) { try await session.auditExportResponse(selection:selection,expectedIdentity:identity) }
            #expect(session.account != nil)
            return
        }
        let first = try #require(state.page)
        if f.role == "admin" {
            #expect(first.total == 55 && first.items.count == 50 && first.nextAfterID == 0)
            let firstIDs = first.items.map(\.id)
            await state.next()
            #expect(state.page?.items.count == 5 && state.page?.total == 55 && state.page?.snapshotID == first.snapshotID)
            await state.previous()
            #expect(state.page?.items.map(\.id) == firstIDs)
            await state.load(query)
            #expect(state.page?.total == 56 && state.page?.items.first?.id != first.items.first?.id)
        } else {
            #expect(first.total == 4 && first.items.allSatisfy { $0.userID == f.user_id })
            await #expect(throws:ServerError.forbidden) { try await session.auditPage(query:AuditQuery(action:f.action,all:true)) }
            let token = try #require(vault.read()?.token)
            await #expect(throws:ServerError.forbidden) { try await api.auditPage(origin,token:token,userID:f.user_id,query:AuditQuery(action:f.action,all:true)) }
            let all = AuditExportSelection(query:try AuditQuery(action:f.action,all:true),snapshotID:first.snapshotID,total:4)
            await #expect(throws:ServerError.forbidden) { try await api.auditExportResponse(origin,token:token,userID:f.user_id,selection:all) }
        }
        let root = URL(fileURLWithPath:f.directory)
        await state.export(try #require(state.exportSelection),to:root.appending(path:f.role+"-audit.csv"))
        #expect(state.error == nil)
        let receipt = try #require(state.receipt), data = try Data(contentsOf:receipt.url)
        #expect(!receipt.cleanupIncomplete && !receipt.truncated && receipt.rows == (f.role == "admin" ? 56 : 4))
        #expect(receipt.bytes == Int64(data.count) && receipt.sha256 == TransferDisk.hex(SHA256.hash(data:data)))
        let text = try #require(String(data:data,encoding:.utf8))
        if f.role == "admin" { #expect(text.contains("'=SUM(1,2)") && !text.contains("synthetic-peer-private")) }
        let savedReceipt: [String:Any] = ["bytes":data.count,"sha256":receipt.sha256,"rows":receipt.rows]
        try JSONSerialization.data(withJSONObject:savedReceipt).write(to:root.appending(path:f.role+"-receipt.json"))
        if f.role == "admin" {
            await state.load(try AuditQuery(action:f.action,all:true))
            #expect(state.page?.total == 61 && state.page?.items.contains { $0.userID == f.peer_id } == true && state.page?.items.contains { $0.userID == nil } == true)
            await state.export(try #require(state.exportSelection),to:root.appending(path:"all-audit.csv"))
            #expect(state.receipt?.rows == 61)
            await state.load(try AuditQuery(action:f.action,status:"failed"))
            #expect(state.page?.total == 11 && state.page?.items.allSatisfy { $0.status == "failed" } == true)
            let date = try ShareContract.date(f.date)
            await state.load(try AuditQuery(action:f.action,start:date,end:date))
            #expect(state.page?.total == 56)
            let token = try #require(vault.read()?.token)
            let redirect = AuditExportSelection(query:try AuditQuery(action:f.action+"_redirect"),snapshotID:first.snapshotID,total:1)
            await #expect(throws:ServerError.invalidResponse) { try await api.auditExportResponse(origin,token:token,userID:f.user_id,selection:redirect) }
            await state.load(try AuditQuery(action:f.corrupt_action))
            await state.export(try #require(state.exportSelection),to:root.appending(path:"corrupt-audit.csv"))
            #expect(state.receipt == nil && state.error != nil && !FileManager.default.fileExists(atPath:root.appending(path:"corrupt-audit.csv").path))
            await state.load(try AuditQuery(action:f.slow_action))
            let slow = try #require(state.exportSelection)
            let task = Task { await state.export(slow,to:root.appending(path:"cancelled-audit.csv")) }
            let bodyDeadline = ContinuousClock.now.advanced(by:.seconds(8))
            while !state.exporting || state.received == 0 {
                guard ContinuousClock.now < bodyDeadline else { state.cancel(); await task.value; throw ServerError.unavailable }
                try await Task.sleep(for:.milliseconds(10))
            }
            #expect(state.received < state.totalBytes)
            state.cancel()
            let cancelDeadline = ContinuousClock.now.advanced(by:.seconds(3))
            while state.busy { guard ContinuousClock.now < cancelDeadline else { throw ServerError.unavailable }; try await Task.sleep(for:.milliseconds(10)) }
            await task.value
            #expect(state.receipt == nil && state.error?.contains("取消") == true)
        }
        let files = try FileManager.default.contentsOfDirectory(atPath:root.path)
        #expect(!files.contains { $0.hasPrefix(".cloudnest-") || $0 == "cancelled-audit.csv" })
        state.discard(); await session.signOut(); #expect(session.account == nil)
    }
}
