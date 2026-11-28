import Foundation
import Testing
@testable import CloudNestCore

private final class SettingsTestVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ value: SavedSession) throws { saved = value }
    func clear() throws { saved = nil }
}
private final class SettingsCounter: @unchecked Sendable {
    private let lock = NSLock()
    private var value = 0
    func next() -> Int { lock.lock(); defer { lock.unlock() }; value += 1; return value }
}
private final class SettingsProtocol: URLProtocol, @unchecked Sendable {
    static let counter = SettingsCounter()
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let host = request.url!.host!, path = request.url!.path
        if path == "/api/auth/login" {
            respond(["token":"settings-fixture", "token_type":"Bearer", "user":["id":7,"username":"fixture","is_admin":true]]); return
        }
        if path == "/api/resource/health" || path == "/api/auth/logout" { respond(["message":"ok"]); return }
        if request.httpMethod == "PUT" {
            var bytes = request.httpBody ?? Data()
            if let stream = request.httpBodyStream {
                stream.open(); defer { stream.close() }
                var buffer = [UInt8](repeating:0,count:1024)
                while stream.hasBytesAvailable {
                    let n = stream.read(&buffer,maxLength:buffer.count)
                    guard n > 0 else { break }
                    bytes.append(contentsOf:buffer.prefix(n))
                }
            }
            guard let body = (try? JSONSerialization.jsonObject(with:bytes)) as? [String:Any] else {
                Issue.record("Missing settings JSON request body"); respond([:],status:400); return
            }
            let expected = body["expected"] as? [String:Any]
            #expect(expected != nil)
            if path == "/api/settings" { #expect(expected?[SystemSettingKey.shareHours.rawValue] is NSNull) }
            else {
                #expect(Set(expected!.keys) == SettingsContract.windowKeys(userID:7))
                #expect(expected!.values.allSatisfy { $0 is NSNull })
            }
            respond(["message":"saved"]); return
        }
        if path == "/api/settings" {
            #expect(URLComponents(url:request.url!,resolvingAgainstBaseURL:false)?.queryItems == [URLQueryItem(name:"scope",value:"system")])
            if host == "clear.settings.test" && Self.counter.next() > 1 { respond([:],status:403); return }
            let key = host == "unknown.settings.test" ? "private_key" : "share_default_expire_hours"
            let row = ["setting_key":key,"setting_value":"24"]
            respond(["items":host == "duplicate.settings.test" ? [row,row] : [row],"conditional_writes":host != "legacy.settings.test"]); return
        }
        var row: [String:Any] = ["enabled":false,"start":"","end":"","source":"global"]
        if host != "legacy.settings.test" {
            let uid: Int64 = host == "misbound.settings.test" ? 8 : 7
            row["expected"] = Dictionary(uniqueKeysWithValues:SettingsContract.windowKeys(userID:uid).map { ($0,NSNull()) })
        }
        respond(["item":row])
    }
    func respond(_ body: [String:Any], status: Int = 200) {
        client?.urlProtocol(self,didReceive:HTTPURLResponse(url:request.url!,statusCode:status,httpVersion:nil,headerFields:nil)!,cacheStoragePolicy:.notAllowed)
        client?.urlProtocol(self,didLoad:try! JSONSerialization.data(withJSONObject:body))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

@Suite(.serialized)
struct SettingsTests {
    private func api() -> ServerAPI {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [SettingsProtocol.self]
        return ServerAPI(configuration:configuration)
    }
    @Test func valuesHaveExactUnitsAndBounds() {
        #expect(SystemSettingKey.shareHours.accepts("0") && !SystemSettingKey.uploadRate.accepts("0"))
        #expect(SystemSettingKey.quotaBytes.accepts("4611686018427387904") && !SystemSettingKey.quotaBytes.accepts("4611686018427387905"))
        #expect(!SystemSettingKey.uploadRate.accepts("+64") && !SystemSettingKey.uploadRate.accepts(" 64"))
        #expect(SettingsContract.validTime("00:00") && SettingsContract.validTime("23:59"))
        #expect(!SettingsContract.validTime("24:00") && !SettingsContract.validTime("1:00") && !SettingsContract.validTime("１２:00"))
    }

    @Test func scopedSystemReadRejectsPrivateAndDuplicateRows() async throws {
        let api = api(), origin = try ServerOrigin("https://good.settings.test")
        let snapshot = try await api.systemSettings(origin,token:"fixture")
        #expect(snapshot.conditionalWrites && snapshot.items.first?.key == .shareHours)
        for host in ["unknown.settings.test","duplicate.settings.test"] {
            do { _ = try await api.systemSettings(ServerOrigin("https://"+host),token:"fixture"); Issue.record("Malformed settings accepted") }
            catch { #expect(error as? ServerError == .invalidResponse) }
        }
    }

    @Test func absentValuesRemainExplicitNullInConditionalWrites() async throws {
        let api = api(), origin = try ServerOrigin("https://good.settings.test")
        try await api.saveSystemSetting(origin,token:"fixture",key:.shareHours,value:"24",original:nil)
        let window = try await api.rateWindow(origin,token:"fixture",userID:7)
        #expect(window.expected?.count == 6)
        try await api.saveRateWindow(origin,token:"fixture",userID:7,original:window,enabled:true,start:"23:00",end:"06:00")
    }

    @Test func legacyAndMisboundRateSnapshotsCannotBeWritten() async throws {
        let api = api(), origin = try ServerOrigin("https://legacy.settings.test")
        #expect(try await api.systemSettings(origin,token:"fixture").conditionalWrites == false)
        let old = try await api.rateWindow(origin,token:"fixture",userID:7)
        #expect(old.expected == nil)
        await #expect(throws:ServerError.invalidOperation) { try await api.saveRateWindow(origin,token:"fixture",userID:7,original:old,enabled:false,start:"",end:"") }
        await #expect(throws:ServerError.invalidResponse) { try await api.rateWindow(ServerOrigin("https://misbound.settings.test"),token:"fixture",userID:7) }
    }

    @Test @MainActor func permissionFailureClearsSettingsAndOldIdentityCannotWrite() async throws {
        let session = SessionState(api:api(),vault:SettingsTestVault())
        await session.restore(); await session.signIn(server:"https://clear.settings.test",username:"fixture",password:"fixture")
        let identity = try #require(session.transferIdentity), state = SettingsState(session:session)
        await state.load(); #expect(state.system != nil && state.window != nil)
        await state.load(); #expect(state.system == nil && state.window == nil && state.error == ServerError.forbidden.localizedDescription && session.account != nil)
        await session.signOut()
        await session.signIn(server:"https://other.settings.test",username:"fixture",password:"fixture")
        #expect(session.transferIdentity?.server == "https://other.settings.test")
        await #expect(throws:ServerError.sessionChanged) { try await session.saveSystemSetting(key:.shareHours,value:"24",original:nil,expectedIdentity:identity) }
        state.cancel()
    }

    @Test @MainActor func conflictAndUnknownReceiptsPreventSameFormReplay() async {
        for error in [ServerError.conflict,.unavailable] {
            let editor = SettingsEditState(); var calls = 0
            await editor.save { calls += 1; throw error }
            await editor.save { calls += 1 }
            #expect(calls == 1 && editor.locked && !editor.busy && !editor.saved && editor.error != nil)
        }
    }
}
