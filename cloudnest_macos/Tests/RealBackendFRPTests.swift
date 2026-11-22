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
    @MainActor func realBackendFRPConfigPreviewSaveAndHistory() async throws {
        let env = ProcessInfo.processInfo.environment
        let origin = try ServerOrigin(#require(env["CLOUDNEST_SWIFT_ORIGIN"]))
        let certificate = try #require(env["CLOUDNEST_SWIFT_CERTIFICATE"])
        let delegate = try FixtureTLSDelegate(der: Data(contentsOf: URL(fileURLWithPath: certificate)))
        let transport = URLSession(configuration: .ephemeral, delegate: delegate, delegateQueue: nil)
        defer { transport.invalidateAndCancel() }
        let session = SessionState(api: ServerAPI(session: transport, delegate: delegate), vault: FRPTestVault())
        await session.restore()
        await session.signIn(server: origin.url.absoluteString, username: try #require(env["CLOUDNEST_SWIFT_USERNAME"]),
                             password: try #require(env["CLOUDNEST_SWIFT_PASSWORD"]))
        let deviceText = try #require(env["CLOUDNEST_SWIFT_FRP_DEVICE"])
        let sourceText = try #require(env["CLOUDNEST_SWIFT_FRP_CONFIG"])
        let deviceID = try #require(Int64(deviceText))
        let sourceID = try #require(Int64(sourceText))
        let builder = FRPConfigState(session: session)
        if env["CLOUDNEST_SWIFT_FRP_ROLE"] == "restricted" {
            await builder.loadPreview(FRPConfigSelection(device_id: deviceID, server_id: 1, server_revision_id: 1))
            #expect(builder.preview == nil)
            #expect(builder.error == ServerError.forbidden.localizedDescription)
            do {
                _ = try await session.rollbackFRPConfig(configID: sourceID, deviceID: deviceID, expectedIdentity: session.transferIdentity)
                Issue.record("Restricted account appended a historical configuration")
            } catch { #expect(error as? ServerError == .forbidden) }
            #expect(session.account != nil)
        } else {
            let configs = try await session.frpConfigs(deviceID: deviceID)
            let commandsBefore = try await session.frpCommands(deviceID: deviceID)
            let original = try #require(configs.first { $0.id == sourceID })
            let server = try await session.frpServer(id: original.serverID)
            let selection = FRPConfigSelection(device_id: deviceID, server_id: server.id, server_revision_id: server.revisionID)
            await builder.loadPreview(selection)
            let preview = try #require(builder.preview)
            #expect(preview.contains("[redacted]"))
            #expect(preview.contains("32000"))
            #expect(builder.error == nil)
            await builder.save(expectedSelection: selection)
            let saved = try #require(builder.saved)
            #expect(saved.id != original.id)
            #expect(saved.serverRevisionID == server.revisionID)
            #expect(builder.preview == nil)
            // Double save through the same state cannot append another row.
            await builder.save(expectedSelection: selection)
            let afterSave = try await session.frpConfigs(deviceID: deviceID)
            #expect(afterSave.count == configs.count + 1)
            let restored = try await session.rollbackFRPConfig(configID: original.id, deviceID: deviceID, expectedIdentity: session.transferIdentity)
            #expect(restored.id != saved.id && restored.id != original.id)
            #expect(restored.serverID == original.serverID)
            #expect(restored.serverRevisionID == original.serverRevisionID)
            let afterRollback = try await session.frpConfigs(deviceID: deviceID)
            #expect(afterRollback.count == configs.count + 2)
            #expect(afterRollback.contains { $0.id == original.id })
            let commandsAfter = try await session.frpCommands(deviceID: deviceID)
            #expect(commandsAfter.map(\.id) == commandsBefore.map(\.id))
            let advanced = FRPServerInput(name: server.name, server_addr: server.address, server_port: server.port,
                tcp_start: server.tcpStart, tcp_end: server.tcpEnd, http_port: server.httpPort, https_port: server.httpsPort,
                enabled: server.enabled, expected_revision_id: server.revisionID, token: nil)
            _ = try await session.writeFRPServer(id: server.id, input: advanced, expectedIdentity: session.transferIdentity)
            let stale = FRPConfigState(session: session)
            await stale.loadPreview(selection)
            #expect(stale.preview == nil)
            #expect(stale.error?.contains("服务器版本") == true)
            #expect(session.account != nil)
        }
        await session.signOut()
    }

    @Test(.enabled(if: ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_FRP_TEST"] == "1"))
    @MainActor func realBackendFRPServerRevisionAndSecretChoices() async throws {
        let env = ProcessInfo.processInfo.environment
        let origin = try ServerOrigin(#require(env["CLOUDNEST_SWIFT_ORIGIN"]))
        let certificate = try #require(env["CLOUDNEST_SWIFT_CERTIFICATE"])
        let delegate = try FixtureTLSDelegate(der: Data(contentsOf: URL(fileURLWithPath: certificate)))
        let transport = URLSession(configuration: .ephemeral, delegate: delegate, delegateQueue: nil)
        defer { transport.invalidateAndCancel() }
        let state = SessionState(api: ServerAPI(session: transport, delegate: delegate), vault: FRPTestVault())
        await state.restore()
        await state.signIn(server: origin.url.absoluteString, username: try #require(env["CLOUDNEST_SWIFT_USERNAME"]),
                           password: try #require(env["CLOUDNEST_SWIFT_PASSWORD"]))
        let browser = FRPServerState(session: state)
        await browser.load()
        if env["CLOUDNEST_SWIFT_FRP_ROLE"] == "restricted" {
            #expect(browser.items.isEmpty)
            #expect(browser.error == ServerError.forbidden.localizedDescription)
            #expect(state.account != nil)
            await state.signOut()
            return
        }
        #expect(browser.error == nil)
        let identity = try #require(state.transferIdentity)
        let host = "native-\(UUID().uuidString.lowercased()).test"
        func input(_ revision: Int64, token: String?, name: String = "Native server") -> FRPServerInput {
            FRPServerInput(name: name, server_addr: host, server_port: 7001, tcp_start: 36000, tcp_end: 36099,
                           http_port: 18081, https_port: 18444, enabled: true, expected_revision_id: revision, token: token)
        }
        let created = try await state.writeFRPServer(id: nil, input: input(0, token: "disposable-native-secret"), expectedIdentity: identity)
        #expect(created.tokenConfigured)
        #expect(created.address == host)
        let kept = try await state.writeFRPServer(id: created.id, input: input(created.revisionID, token: nil, name: "Kept secret"), expectedIdentity: identity)
        #expect(kept.tokenConfigured)
        #expect(kept.revisionID != created.revisionID)
        do {
            _ = try await state.writeFRPServer(id: created.id, input: input(created.revisionID, token: "stale-secret"), expectedIdentity: identity)
            Issue.record("An old server revision overwrote a newer revision")
        } catch { #expect(error as? ServerError == .conflict) }
        let unchanged = try await state.frpServer(id: created.id)
        #expect(unchanged.revisionID == kept.revisionID)
        #expect(unchanged.name == "Kept secret")
        let cleared = try await state.writeFRPServer(id: created.id, input: input(kept.revisionID, token: ""), expectedIdentity: identity)
        #expect(!cleared.tokenConfigured)
        let replaced = try await state.writeFRPServer(id: created.id, input: input(cleared.revisionID, token: "replacement-native-secret"), expectedIdentity: identity)
        #expect(replaced.tokenConfigured)
        let page = try await state.frpServers(afterID: created.id - 1)
        #expect(page.items.first?.id == created.id)
        #expect(page.nextAfterID == page.items.last?.id)
        let empty = try await state.frpServers(afterID: try #require(page.items.last?.id))
        #expect(empty.items.isEmpty)
        #expect(empty.nextAfterID == 0)
        await state.signOut()
        await browser.load()
        #expect(browser.items.isEmpty)
        #expect(browser.error == ServerError.unauthorized.localizedDescription)
    }

    @Test(.enabled(if: ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_FRP_TEST"] == "1"))
    @MainActor func realBackendFRPCommandReceiptRecovery() async throws {
        let env = ProcessInfo.processInfo.environment
        let origin = try ServerOrigin(#require(env["CLOUDNEST_SWIFT_ORIGIN"]))
        let certificate = try #require(env["CLOUDNEST_SWIFT_CERTIFICATE"])
        let delegate = try FixtureTLSDelegate(der: Data(contentsOf: URL(fileURLWithPath: certificate)))
        let configuration = URLSessionConfiguration.ephemeral
        configuration.httpAdditionalHeaders = ["X-CloudNest-Test-FRP-Lost": "native-command-receipt"]
        let transport = URLSession(configuration: configuration, delegate: delegate, delegateQueue: nil)
        defer { transport.invalidateAndCancel() }
        let state = SessionState(api: ServerAPI(session: transport, delegate: delegate), vault: FRPTestVault())
        await state.restore()
        let username = try #require(env["CLOUDNEST_SWIFT_USERNAME"])
        let password = try #require(env["CLOUDNEST_SWIFT_PASSWORD"])
        await state.signIn(server: origin.url.absoluteString, username: username, password: password)
        let value = try #require(env["CLOUDNEST_SWIFT_FRP_DEVICE"])
        let deviceID = try #require(Int64(value))
        let root = FileManager.default.temporaryDirectory.appending(path: "cloudnest-frp-receipt-\(UUID())")
        defer { try? FileManager.default.removeItem(at: root) }
        var manager = FRPCommandManager(session: state, directory: root)
        await manager.restore()
        let identity = try #require(state.transferIdentity)
        let id = try await manager.submit(deviceID: deviceID, action: .diagnose, expectedIdentity: identity)
        let uncertain = try #require(manager.records[id])
        #expect(uncertain.outcome == .unconfirmed)
        #expect(uncertain.commandID == nil)
        #expect(uncertain.error != nil)
        #expect(state.account != nil)
        if env["CLOUDNEST_SWIFT_FRP_ROLE"] == "restricted" {
            #expect(uncertain.error?.contains(ServerError.forbidden.localizedDescription) == true)
            try await manager.abandon(id)
            #expect(manager.records[id]?.outcome == .abandoned)
            await manager.close()
        } else {
            let rows = try await state.frpCommands(deviceID: deviceID)
            let remote = try #require(rows.first { $0.requestID == uncertain.requestID })
            #expect(rows.filter { $0.requestID == uncertain.requestID }.count == 1)
            #expect(remote.status == "pending")
            do {
                _ = try await manager.submit(deviceID: deviceID, action: .diagnose, expectedIdentity: identity)
                Issue.record("An unconfirmed command was recreated with another request ID")
            } catch { #expect(error as? FRPCommandStoreError == .conflict) }
            await manager.close()
            manager = FRPCommandManager(session: state, directory: root)
            await manager.restore()
            #expect(manager.records[id] == uncertain)
            // Reopening never automatically submits or changes the journal.
            _ = try await manager.retry(id)
            #expect(manager.records[id]?.outcome == .accepted)
            #expect(manager.records[id]?.commandID == remote.id)
            #expect(manager.records[id]?.requestID == uncertain.requestID)
            let after = try await state.frpCommands(deviceID: deviceID)
            #expect(after.filter { $0.requestID == uncertain.requestID }.count == 1)
            await manager.close()
        }
        await state.signOut()
    }

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
            let sourceConfigID = try number("CLOUDNEST_SWIFT_FRP_CONFIG")
            let originalConfig = try #require(configs.first { $0.id == sourceConfigID })
            #expect(originalConfig.serverID == mappings.first?.serverID)
            await detail.load(.commands)
            #expect(detail.error == nil)
            guard case .commands(let commands) = detail.snapshot else { Issue.record("Missing command metadata"); return }
            let seededID = try #require(env["CLOUDNEST_SWIFT_FRP_COMMAND"])
            let seeded = try #require(commands.first { $0.id == seededID })
            #expect(seeded.status == "pending")
            #expect(seeded.action == "diagnose")
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
