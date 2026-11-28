import Foundation
import Testing
@testable import CloudNestCore

private final class SettingsBackendVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ value: SavedSession) throws { saved = value }
    func clear() throws { saved = nil }
}

extension RealBackendTests {
    @Test(.enabled(if: ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_SETTINGS_TEST"] == "1"))
    @MainActor func realBackendSettings() async throws {
        let env = ProcessInfo.processInfo.environment, origin = try ServerOrigin(#require(env["CLOUDNEST_SWIFT_ORIGIN"]))
        let certificate = try #require(env["CLOUDNEST_SWIFT_CERTIFICATE"])
        let delegate = try FixtureTLSDelegate(der:Data(contentsOf:URL(fileURLWithPath:certificate)))
        let transport = URLSession(configuration:.ephemeral,delegate:delegate,delegateQueue:nil)
        defer { transport.invalidateAndCancel() }
        let api = ServerAPI(session:transport,delegate:delegate), vault = SettingsBackendVault()
        let session = SessionState(api:api,vault:vault)
        await session.restore()
        await session.signIn(server:origin.url.absoluteString,username:try #require(env["CLOUDNEST_SWIFT_USERNAME"]),password:try #require(env["CLOUDNEST_SWIFT_PASSWORD"]))
        let identity = try #require(session.transferIdentity), state = SettingsState(session:session)
        await state.load()
        if env["CLOUDNEST_SWIFT_SETTINGS_ROLE"] == "restricted" {
            #expect(state.system == nil && state.window == nil && state.error == ServerError.forbidden.localizedDescription && session.account != nil)
            return
        }
        let settings = try #require(state.system), window = try #require(state.window)
        #expect(settings.conditionalWrites && window.expected?.count == 6)
        let token = try #require(vault.read()?.token)
        if env["CLOUDNEST_SWIFT_SETTINGS_ROLE"] == "admin" {
            #expect(session.account?.isAdmin == true)
            let original = settings.items.first { $0.key == .shareHours }?.value
            #expect(original == nil)
            // A separate API write advances content after the native form read.
            try await api.saveSystemSetting(origin,token:token,key:.shareHours,value:"48",original:original)
            let editor = SettingsEditState()
            await editor.save { try await session.saveSystemSetting(key:.shareHours,value:"72",original:original,expectedIdentity:identity) }
            #expect(editor.locked && !editor.saved && editor.error?.contains("刷新") == true)
            await state.load()
            let fresh = try #require(state.system?.items.first { $0.key == .shareHours })
            #expect(fresh.value == "48")
            try await session.saveSystemSetting(key:.shareHours,value:"72",original:fresh.value,expectedIdentity:identity)
            #expect(try await session.systemSettings().items.first { $0.key == .shareHours }?.value == "72")
        } else {
            #expect(session.account?.isAdmin == false)
            await #expect(throws:ServerError.forbidden) { try await session.saveSystemSetting(key:.shareHours,value:"96",original:"72",expectedIdentity:identity) }
            await #expect(throws:ServerError.forbidden) { try await api.saveSystemSetting(origin,token:token,key:.shareHours,value:"96",original:"72") }
        }
        try await api.saveRateWindow(origin,token:token,userID:identity.userID,original:window,enabled:true,start:"23:00",end:"06:00")
        await #expect(throws:ServerError.conflict) { try await session.saveRateWindow(original:window,enabled:true,start:"08:00",end:"09:00",expectedIdentity:identity) }
        let fresh = try await session.rateWindow()
        #expect(fresh.source == "user" && fresh.enabled && fresh.start == "23:00" && fresh.end == "06:00")
        try await session.saveRateWindow(original:fresh,enabled:false,start:"",end:"",expectedIdentity:identity)
        let disabled = try await session.rateWindow()
        #expect(!disabled.enabled && disabled.source == "user" && session.account != nil)
        state.cancel(); await session.signOut()
        #expect(session.account == nil)
    }
}
