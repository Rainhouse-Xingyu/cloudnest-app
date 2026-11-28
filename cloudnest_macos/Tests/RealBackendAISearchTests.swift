import Foundation
import Testing
@testable import CloudNestCore

private final class AISearchTestVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ session: SavedSession) throws { saved = session }
    func clear() throws { saved = nil }
}

extension RealBackendTests {
    @Test(.enabled(if: ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_AI_LIVE"] == "1"))
    @MainActor func realBackendLiveAISemanticSearch() async throws {
        let env = ProcessInfo.processInfo.environment
        let origin = try ServerOrigin(#require(env["CLOUDNEST_SWIFT_ORIGIN"]))
        let certificate = try #require(env["CLOUDNEST_SWIFT_CERTIFICATE"])
        let delegate = try FixtureTLSDelegate(der: Data(contentsOf: URL(fileURLWithPath: certificate)))
        let transport = URLSession(configuration: .ephemeral, delegate: delegate, delegateQueue: nil)
        defer { transport.invalidateAndCancel() }
        let state = SessionState(api: ServerAPI(session: transport, delegate: delegate), vault: AISearchTestVault())
        await state.restore()
        let username = try #require(env["CLOUDNEST_SWIFT_USERNAME"])
        let password = try #require(env["CLOUDNEST_SWIFT_PASSWORD"])
        await state.signIn(server: origin.url.absoluteString, username: username, password: password)
        let search = AISearchState(session: state)
        search.question = "MySQL 升级与数据迁移的执行计划"
        await search.submit()
        #expect(search.error == nil)
        let response = try #require(search.response)
        #expect(response.retrieval.mode == "hybrid")
        #expect(!response.retrieval.warnings.contains("embedding_unavailable"))
        let ownValue = try #require(env["CLOUDNEST_SWIFT_SEARCH_FILE"])
        let ownID = try #require(Int64(ownValue))
        #expect(response.items.map(\.id) == [ownID])
        let hit = try #require(response.items.first)
        #expect(hit.matchSources.contains("semantic"))
        #expect(hit.snippet.contains("MySQL 升级前先备份"))
        #expect(hit.snippetOffsetBasis == "extracted_text_runes")
        #expect(hit.indexGeneration > 0)
        let index = AIIndexState(session: state)
        await index.load()
        #expect(index.error == nil)
        let item = try #require(index.items.first { $0.id == ownID })
        #expect(item.indexedCurrent)
        #expect(item.sourceCurrent)
        #expect(item.chunkCount == 1)
        #expect(!index.items.contains { $0.filename == "Private Atlas.md" })
        let root = FileManager.default.temporaryDirectory.appending(path: "cloudnest-search-download-\(UUID())")
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let transfers = TransferManager(session: state, directory: root.appending(path: "journal"))
        let destination = root.appending(path: "Atlas.md")
        let task = try await transfers.enqueueDownload(hit, destination: destination, expectedIdentity: state.transferIdentity)
        await transfers.waitUntilIdle(task)
        #expect(transfers.records[task]?.phase == .completed)
        let downloaded = try String(contentsOf: destination, encoding: .utf8)
        #expect(downloaded == "数据库迁移方案：MySQL 升级前先备份，并验证迁移后的数据一致性。迁移失败时回滚到已验证的备份。")
        await transfers.close()
        await state.signOut()
    }

    @Test(.enabled(if: ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_ORIGIN"] != nil))
    @MainActor func realBackendAISearchAndIndexControl() async throws {
        let env = ProcessInfo.processInfo.environment
        let origin = try ServerOrigin(#require(env["CLOUDNEST_SWIFT_ORIGIN"]))
        let delegate = try FixtureTLSDelegate(der: Data(contentsOf: URL(fileURLWithPath: #require(env["CLOUDNEST_SWIFT_CERTIFICATE"]))))
        let transport = URLSession(configuration: .ephemeral, delegate: delegate, delegateQueue: nil)
        defer { transport.invalidateAndCancel() }
        let api = ServerAPI(session: transport, delegate: delegate)
        let state = SessionState(api: api, vault: AISearchTestVault())
        await state.restore()
        let username = try #require(env["CLOUDNEST_SWIFT_USERNAME"])
        let password = try #require(env["CLOUDNEST_SWIFT_PASSWORD"])
        await state.signIn(server: origin.url.absoluteString, username: username, password: password)
        let search = AISearchState(session: state)
        search.question = "CloudNestSemanticFixtureZX9182"
        await search.submit()
        let index = AIIndexState(session: state)
        await index.load()
        if env["CLOUDNEST_SWIFT_RESTRICTED"] == "1" {
            #expect(search.response == nil)
            #expect(search.error == ServerError.forbidden.localizedDescription)
            #expect(index.items.isEmpty)
            #expect(index.error == ServerError.forbidden.localizedDescription)
            #expect(state.account != nil)
        } else {
            #expect(search.error == nil)
            let response = try #require(search.response)
            let ownValue = try #require(env["CLOUDNEST_SWIFT_SEARCH_FILE"])
            let otherValue = try #require(env["CLOUDNEST_SWIFT_SEARCH_OTHER"])
            let ownID = try #require(Int64(ownValue))
            let otherID = try #require(Int64(otherValue))
            #expect(response.items.map(\.id) == [ownID])
            #expect(response.retrieval.mode == "keyword_fallback")
            #expect(response.retrieval.warnings.contains("embedding_unavailable"))
            #expect(response.items.first?.matchSources == ["keyword"])
            #expect(response.items.first?.snippet.isEmpty == true)
            #expect(!search.hasNext)
            #expect(index.error == nil)
            #expect(!index.items.contains { $0.id == otherID })
            let original = try #require(index.items.first { $0.id == ownID })
            #expect(!original.indexedCurrent)
            // A second client rebuilds after this window read the generation.
            try await state.rebuildAI(original)
            await index.rebuild(original)
            #expect(index.error == "索引已变化，已重新读取状态。请核对后再次选择重建。")
            #expect(index.notice == nil)
            let refreshed = try #require(index.items.first { $0.id == ownID })
            #expect(refreshed.generation == original.generation + 1)
            await index.rebuild(refreshed)
            #expect(index.error == nil)
            #expect(index.notice != nil)
            #expect(index.items.first { $0.id == ownID }?.generation == original.generation + 2)
            // An old generation must not silently restart the queue a second time.
            do {
                try await state.rebuildAI(original)
                Issue.record("A stale rebuild was accepted")
            } catch { #expect(error as? ServerError == .conflict) }
            #expect(state.account != nil)
        }
        await state.signOut()
    }
}
