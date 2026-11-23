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
    @MainActor func realBackendFRPMappingLifecycleAndHistoryRetention() async throws {
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
        let identity = try #require(session.transferIdentity)
        let sourceText = try #require(env["CLOUDNEST_SWIFT_FRP_DEVICE"])
        let sourceID = try #require(Int64(sourceText))
        if env["CLOUDNEST_SWIFT_FRP_ROLE"] == "restricted" {
            do {
                _ = try await session.writeFRPMapping(original: nil, deviceID: sourceID, serverID: 1, localPort: 8123,
                    remotePort: nil, domain: "", enabled: true, expectedIdentity: identity)
                Issue.record("Restricted account created a mapping")
            } catch { #expect(error as? ServerError == .forbidden) }
            #expect(session.account != nil)
        } else {
            let devices = try await session.frpDevices()
            let spare = try #require(devices.first { $0.id != sourceID })
            let legacyText = try #require(env["CLOUDNEST_SWIFT_FRP_LEGACY_MAPPING"])
            let legacyID = try #require(Int64(legacyText))
            let oldMappings = try await session.frpMappings(deviceID: spare.id)
            let legacy = try #require(oldMappings.first { $0.id == legacyID })
            let legacyEdited = try await session.writeFRPMapping(original: legacy, deviceID: spare.id, serverID: 0,
                localPort: 8091, remotePort: legacy.remotePort, domain: "http://Legacy-Revised.Example.", enabled: true, expectedIdentity: identity)
            #expect(legacyEdited.serverID == 0 && legacyEdited.remotePort == legacy.remotePort)
            #expect(legacyEdited.domain == "legacy-revised.example")
            try await session.deleteFRPMapping(id: legacy.id, expectedIdentity: identity)
            let configs = try await session.frpConfigs(deviceID: sourceID)
            let source = try #require(configs.first)
            let server = try await session.frpServer(id: source.serverID)
            let created = try await session.writeFRPMapping(original: nil, deviceID: spare.id, serverID: server.id,
                localPort: 8123, remotePort: nil, domain: "", enabled: false, expectedIdentity: identity)
            #expect(created.remotePort >= server.tcpStart && created.remotePort <= server.tcpEnd)
            #expect(created.status == 0)
            let changed = try await session.writeFRPMapping(original: created, deviceID: spare.id, serverID: server.id,
                localPort: 8124, remotePort: created.remotePort, domain: "", enabled: true, expectedIdentity: identity)
            do {
                _ = try await session.writeFRPMapping(original: changed, deviceID: sourceID, serverID: server.id,
                    localPort: 8225, remotePort: changed.remotePort, domain: "", enabled: true, expectedIdentity: identity)
                Issue.record("Mapping editor submitted under a different device selection")
            } catch { #expect(error as? ServerError == .invalidOperation) }
            let boundRows = try await session.frpMappings(deviceID: spare.id)
            #expect(boundRows.first { $0.id == changed.id }?.localPort == changed.localPort)
            do {
                _ = try await session.writeFRPMapping(original: created, deviceID: spare.id, serverID: server.id,
                    localPort: 8125, remotePort: created.remotePort, domain: "", enabled: true, expectedIdentity: identity)
                Issue.record("Stale mapping edit overwrote newer fields")
            } catch { #expect(error as? ServerError == .conflict) }
            let secure = try await session.writeFRPMapping(original: changed, deviceID: spare.id, serverID: server.id,
                localPort: 8124, remotePort: nil, domain: "HTTPS://Native-Maps.Example.", enabled: true, expectedIdentity: identity)
            #expect(secure.remotePort == 0 && secure.domain == "https://native-maps.example")
            let plain = try await session.writeFRPMapping(original: nil, deviceID: spare.id, serverID: server.id,
                localPort: 8126, remotePort: nil, domain: "http://Native-Http.Example.", enabled: true, expectedIdentity: identity)
            #expect(plain.remotePort == 0 && plain.domain == "native-http.example")
            let latest = try await session.frpServer(id: server.id)
            let saved = try await session.saveFRPConfig(FRPConfigSelection(device_id: spare.id, server_id: server.id,
                server_revision_id: latest.revisionID), expectedIdentity: identity)
            let tcp = try await session.writeFRPMapping(original: secure, deviceID: spare.id, serverID: server.id,
                localPort: 8124, remotePort: nil, domain: "", enabled: true, expectedIdentity: identity)
            #expect(tcp.remotePort >= server.tcpStart && tcp.remotePort <= server.tcpEnd)
            #expect(tcp.domain.isEmpty)
            try await session.deleteFRPMapping(id: plain.id, expectedIdentity: identity)
            try await session.deleteFRPMapping(id: tcp.id, expectedIdentity: identity)
            let after = try await session.frpMappings(deviceID: spare.id)
            #expect(after.isEmpty)
            let retained = try await session.frpConfigs(deviceID: spare.id)
            #expect(retained.contains { $0.id == saved.id })
            let commands = try await session.frpCommands(deviceID: spare.id)
            #expect(commands.isEmpty)
        }
        await session.signOut()
    }

    @Test(.enabled(if: ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_FRP_TEST"] == "1"))
    @MainActor func realBackendFRPDeviceLifecycle() async throws {
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
        let identity = try #require(session.transferIdentity)
        if env["CLOUDNEST_SWIFT_FRP_ROLE"] == "restricted" {
            do {
                _ = try await session.writeFRPDevice(original: nil, name: "Denied device", address: "", enabled: true, expectedIdentity: identity)
                Issue.record("Restricted account created a device")
            } catch { #expect(error as? ServerError == .forbidden) }
            #expect(session.account != nil)
        } else {
            let created = try await session.writeFRPDevice(original: nil, name: "Native created device", address: "198.51.100.42", enabled: true, expectedIdentity: identity)
            #expect(created.status == 1 && !created.isRetired)
            let changed = try await session.writeFRPDevice(original: created, name: "Native changed device", address: "", enabled: false, expectedIdentity: identity)
            #expect(changed.address.isEmpty && changed.status == 0)
            do {
                _ = try await session.writeFRPDevice(original: created, name: "Stale native device", address: created.address, enabled: true, expectedIdentity: identity)
                Issue.record("A stale device edit overwrote newer content")
            } catch { #expect(error as? ServerError == .conflict) }
            let active = try await session.frpDevices()
            let current = try #require(active.first { $0.id == created.id })
            #expect(current.name == changed.name && current.address.isEmpty && current.status == 0)
            try await session.retireFRPDevice(id: current.id, expectedIdentity: identity)
            let after = try await session.frpDevices()
            #expect(!after.contains { $0.id == current.id })
            let retired = try await session.frpDevices(retired: true)
            let preserved = try #require(retired.first { $0.id == current.id })
            #expect(preserved.isRetired && preserved.status == 0)
            let archive = try await session.frpDeviceArchive(id: current.id)
            #expect(archive.device.name == changed.name)
            #expect(archive.device.isRetired)
            #expect(archive.mappingCount == 0 && archive.latestConfigTime == nil)
            do {
                _ = try await session.writeFRPDevice(original: changed, name: "Reactivate retired", address: "", enabled: true, expectedIdentity: identity)
                Issue.record("Retired device was reactivated by an old editor")
            } catch { #expect(error as? ServerError == .notFound) }
        }
        await session.signOut()
    }

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
