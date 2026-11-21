import Foundation
import Testing
@testable import CloudNestCore

private final class FRPTestVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ session: SavedSession) throws { saved = session }
    func clear() throws { saved = nil }
}

extension RealBackendTests {
    @Test(.enabled(if: ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_FRP_TEST"] == "1"))
    @MainActor func realBackendFRPRead() async throws {
        let env = ProcessInfo.processInfo.environment
        let origin = try ServerOrigin(#require(env["CLOUDNEST_SWIFT_ORIGIN"]))
        let certificate = try #require(env["CLOUDNEST_SWIFT_CERTIFICATE"])
        let delegate = try FixtureTLSDelegate(der: Data(contentsOf: URL(fileURLWithPath: certificate)))
        let transport = URLSession(configuration: .ephemeral, delegate: delegate, delegateQueue: nil)
        defer { transport.invalidateAndCancel() }
        let state = SessionState(api: ServerAPI(session: transport, delegate: delegate), vault: FRPTestVault())
        await state.restore()
        let username = try #require(env["CLOUDNEST_SWIFT_USERNAME"])
        let password = try #require(env["CLOUDNEST_SWIFT_PASSWORD"])
        await state.signIn(server: origin.url.absoluteString, username: username, password: password)
        let browser = FRPBrowserState(session: state)
        await browser.reload()
        if env["CLOUDNEST_SWIFT_FRP_ROLE"] == "restricted" {
            #expect(browser.devices.isEmpty)
            #expect(browser.error == ServerError.forbidden.localizedDescription)
            #expect(state.account != nil)
        } else {
            #expect(browser.error == nil)
            func number(_ key: String) throws -> Int64 {
                let raw = try #require(env[key])
                return try #require(Int64(raw))
            }
            let deviceID = try number("CLOUDNEST_SWIFT_FRP_DEVICE")
            let otherID = try number("CLOUDNEST_SWIFT_FRP_OTHER")
            #expect(!browser.devices.contains { $0.id == otherID })
            let device = try #require(browser.devices.first { $0.id == deviceID })
            let detail = FRPDetailState(device: device, session: state)
            await detail.load(.status)
            #expect(detail.error == nil)
            guard case .status(let status) = detail.snapshot else { Issue.record("Missing device status"); return }
            #expect(!status.online)
            #expect(status.runtime == nil)
            await detail.load(.mappings)
            #expect(detail.error == nil)
            guard case .mappings(let mappings) = detail.snapshot else { Issue.record("Missing mappings"); return }
            #expect(mappings.map(\.id) == [try number("CLOUDNEST_SWIFT_FRP_MAPPING")])
            #expect(mappings.first?.localPort == 8080)
            #expect(mappings.first?.remotePort == 32000)
            await detail.load(.configs)
            #expect(detail.error == nil)
            guard case .configs(let configs) = detail.snapshot else { Issue.record("Missing config metadata"); return }
            #expect(configs.map(\.id) == [try number("CLOUDNEST_SWIFT_FRP_CONFIG")])
            #expect(configs.first?.serverID == mappings.first?.serverID)
            await detail.load(.commands)
            #expect(detail.error == nil)
            guard case .commands(let commands) = detail.snapshot else { Issue.record("Missing command metadata"); return }
            #expect(commands.map(\.id) == [try #require(env["CLOUDNEST_SWIFT_FRP_COMMAND"])])
            #expect(commands.first?.status == "pending")
            #expect(commands.first?.action == "diagnose")
            do {
                _ = try await state.frpMappings(deviceID: otherID)
                Issue.record("Other user's mapping metadata returned")
            } catch { #expect(error as? ServerError == .forbidden) }
            #expect(state.account != nil)
            await state.signOut()
            await detail.load(.status)
            #expect(detail.snapshot == nil)
            #expect(detail.error == ServerError.unauthorized.localizedDescription)
            await browser.reload()
            #expect(browser.devices.isEmpty)
        }
        await state.signOut()
    }
}
