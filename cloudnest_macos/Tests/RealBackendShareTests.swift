import Foundation
import Testing
@testable import CloudNestCore

private final class ShareTestVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ value: SavedSession) throws { saved = value }
    func clear() throws { saved = nil }
}

extension RealBackendTests {
    @Test(.enabled(if: ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_SHARES_TEST"] == "1"))
    @MainActor func realBackendShares() async throws {
        let env = ProcessInfo.processInfo.environment
        let origin = try ServerOrigin(#require(env["CLOUDNEST_SWIFT_ORIGIN"]))
        let certificate = try #require(env["CLOUDNEST_SWIFT_CERTIFICATE"])
        let delegate = try FixtureTLSDelegate(der: Data(contentsOf: URL(fileURLWithPath: certificate)))
        let transport = URLSession(configuration: .ephemeral, delegate: delegate, delegateQueue: nil)
        defer { transport.invalidateAndCancel() }
        let api = ServerAPI(session: transport, delegate: delegate)
        let owner = SessionState(api: api, vault: ShareTestVault())
        let password = try #require(env["CLOUDNEST_SWIFT_PASSWORD"])
        await owner.restore()
        await owner.signIn(server: origin.url.absoluteString, username: try #require(env["CLOUDNEST_SWIFT_USERNAME"]), password: password)
        let identity = try #require(owner.transferIdentity)
        let state = SharesState(session: owner)
        if env["CLOUDNEST_SWIFT_SHARES_ROLE"] == "restricted" {
            await state.load()
            #expect(state.items.isEmpty && state.error == ServerError.forbidden.localizedDescription && owner.account != nil)
            return
        }
        let idText = try #require(env["CLOUDNEST_SWIFT_SHARES_FILE"])
        let fileID = try #require(Int64(idText))
        let files = try await owner.listFiles(parentID: 0, recycleBin: false)
        let file = try #require(files.first { $0.id == fileID })
        do {
            _ = try await owner.createShare(file: file, expireHours: 24, passcode: "short", maxDownloads: 1, expectedIdentity: identity)
            Issue.record("Invalid passcode reached share creation")
        } catch { #expect(error as? ServerError == .invalidOperation) }
        let passcode = "fixture-passcode"
        let created = try await owner.createShare(file: file, expireHours: 1, passcode: passcode, maxDownloads: 1, expectedIdentity: identity)
        #expect(created.fileID == file.id && created.requiresPasscode && created.maxDownloads == 1 && created.expiresAt != nil)
        await state.load()
        let row = try #require(state.items.first { $0.code == created.code })
        #expect(row.fileName == file.filename && row.fileID == file.id && row.ownerID == identity.userID && row.downloadCount == 0)
        let peerVault = ShareTestVault()
        let peer = SessionState(api: api, vault: peerVault)
        await peer.restore()
        await peer.signIn(server: origin.url.absoluteString, username: try #require(env["CLOUDNEST_SWIFT_SHARES_PEER"]), password: password)
        let peerIdentity = try #require(peer.transferIdentity)
        #expect(try await peer.shares().isEmpty)
        do {
            try await peer.revokeShare(row, expectedIdentity: peerIdentity)
            Issue.record("Native peer could revoke another account's share")
        } catch { #expect(error as? ServerError == .forbidden) }
        do {
            try await api.revokeShare(origin, token: try #require(peerVault.read()?.token), code: created.code)
            Issue.record("Server peer could revoke another account's share")
        } catch { #expect(error as? ServerError == .notFound) }

        // This independently exercises anonymous HTTP against the link produced
        // by the real Core create API; it is not recipient-UI/download-manager proof.
        func anonymous(_ code: String, passcode: String?, download: Bool) async throws -> (Data, HTTPURLResponse) {
            let link = try ShareContract.url(origin: origin, code: code)
            var request = URLRequest(url: download ? link.appending(path: "download") : link)
            if let passcode { request.setValue(passcode, forHTTPHeaderField: "X-Share-Passcode") }
            #expect(request.value(forHTTPHeaderField: "Authorization") == nil && request.url?.query == nil)
            let (data, response) = try await transport.data(for: request, delegate: delegate)
            return (data, try #require(response as? HTTPURLResponse))
        }
        for value in [nil, "wrong-passcode"] as [String?] {
            let (data, response) = try await anonymous(created.code, passcode: value, download: false)
            #expect(response.statusCode == 403 && !String(decoding: data, as: UTF8.self).contains(file.filename))
        }
        let (_, metadata) = try await anonymous(created.code, passcode: passcode, download: false)
        #expect(metadata.statusCode == 200)
        #expect(try await owner.shares().first { $0.id == row.id }?.downloadCount == 0)
        let (body, response) = try await anonymous(created.code, passcode: passcode, download: true)
        #expect(response.statusCode == 200 && body == Data("Native limited public share download.\n".utf8))
        let (_, exhausted) = try await anonymous(created.code, passcode: passcode, download: true)
        #expect(exhausted.statusCode == 410)
        let counted = try await owner.shares()
        #expect(counted.first { $0.id == row.id }?.downloadCount == 1 && counted.first { $0.id == row.id }?.exhausted == true)
        let unlimited = try await owner.createShare(file: file, expireHours: 0, passcode: "", maxDownloads: 0, expectedIdentity: identity)
        #expect(unlimited.expiresAt == nil && !unlimited.requiresPasscode && unlimited.maxDownloads == 0)
        let (_, accessible) = try await anonymous(unlimited.code, passcode: nil, download: false)
        #expect(accessible.statusCode == 200)
        let all = try await owner.shares()
        let unlimitedRow = try #require(all.first { $0.code == unlimited.code })
        try await owner.revokeShare(unlimitedRow, expectedIdentity: identity)
        let (_, revoked) = try await anonymous(unlimited.code, passcode: nil, download: false)
        #expect(revoked.statusCode == 404)
        try await owner.revokeShare(row, expectedIdentity: identity)
        await state.load()
        #expect(state.items.isEmpty && state.error == nil && owner.account != nil)
        state.cancel()
    }
}
