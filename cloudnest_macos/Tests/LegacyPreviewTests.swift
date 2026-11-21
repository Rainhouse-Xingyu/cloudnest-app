import Foundation
import CryptoKit
import Testing
@testable import CloudNestCore

private final class LegacyVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ session: SavedSession) throws { saved = session }
    func clear() throws { saved = nil }
}

private final class LegacyProtocol: URLProtocol, @unchecked Sendable {
    private static let lock = NSLock()
    nonisolated(unsafe) private static var downloads = 0
    static var downloadCount: Int { lock.lock(); defer { lock.unlock() }; return downloads }
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        guard let url = request.url else { return }
        let status: Int
        let body: String
        switch url.path {
        case "/api/auth/login":
            status = 200
            body = #"{"token":"legacy-test","token_type":"Bearer","user":{"id":17,"username":"legacy","email":null,"is_admin":false}}"#
        case "/api/resource/health": status = 403; body = #"{"error":"restricted"}"#
        default:
            if url.path.hasSuffix("/download") { Self.lock.lock(); Self.downloads += 1; Self.lock.unlock() }
            status = 404; body = #"{"error":"not found"}"#
        }
        guard let response = HTTPURLResponse(url: url, statusCode: status, httpVersion: nil, headerFields: nil) else { return }
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(body.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

@MainActor private func legacySession() async -> SessionState {
    let configuration = URLSessionConfiguration.ephemeral
    configuration.protocolClasses = [LegacyProtocol.self]
    let state = SessionState(api: ServerAPI(configuration: configuration), vault: LegacyVault())
    await state.restore()
    await state.signIn(server: "https://legacy-preview.test", username: "fixture", password: "fixture")
    return state
}

@Suite(.serialized)
struct LegacyPreviewTests {
    @Test @MainActor func oldPreviewFilesAndJournalsSurviveRestoreResumeCancelAndClose() async throws {
        let root = FileManager.default.temporaryDirectory.appending(path: "cloudnest-legacy-\(UUID())")
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let state = await legacySession()
        let identity = try #require(state.transferIdentity)
        let disk = TransferDisk()
        var originals: [(TransferRecord, Data, URL, URL)] = []
        for phase: TransferRecord.Phase in [.queued, .running, .paused, .failed, .completed] {
            let folder = root.appending(path: "previews/\(UUID())")
            try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
            // Across signing identities a security-scoped bookmark can fail
            // scoped resolution while its read-only location still resolves.
            let bookmark = (phase == .queued || phase == .running)
                ? try folder.bookmarkData(options: [], includingResourceValuesForKeys: nil, relativeTo: nil)
                : try await disk.bookmark(folder)
            var job = TransferRecord(id: UUID(), createdAt: Date(), identity: identity, direction: .download,
                name: "old.txt", bookmark: bookmark, parentID: 0, fileID: 21, isArchive: false,
                phase: phase, totalBytes: 8)
            if phase == .completed {
                job.totalBytes = 11
                job.confirmedBytes = 11
                job.sha256 = TransferDisk.hex(SHA256.hash(data: Data("keep result".utf8)))
                job.downloadedChunks = [try #require(job.sha256)]
                job.resultName = "old.txt"
            }
            let journal = root.appending(path: "\(job.id).json")
            let data = try JSONEncoder().encode(job)
            try data.write(to: journal)
            let part = folder.appending(path: ".cloudnest-\(job.id).part")
            let result = folder.appending(path: "old.txt")
            try Data("keep partial".utf8).write(to: part)
            try Data("keep result".utf8).write(to: result)
            originals.append((job, data, part, result))
        }
        let unowned = root.appending(path: "previews/unknown.txt")
        try Data("unknown retained".utf8).write(to: unowned)
        let managed = PreviewCache(directory: root.appending(path: "preview-cache-v1"))
        let crashCopy = try await managed.reserve(UUID(), bytes: 5)
        try Data("crash".utf8).write(to: crashCopy.appending(path: "copy"))
        let beforeRequests = LegacyProtocol.downloadCount
        let manager = TransferManager(session: state, directory: root)
        await manager.restore()
        #expect(manager.restored)
        #expect(manager.legacyPreviewJobs == Set(originals.map { $0.0.id }))
        #expect(manager.legacyPreviewDirectory != nil)
        #expect(!FileManager.default.fileExists(atPath: crashCopy.path))
        for (job, _, _, _) in originals {
            await manager.resume(job.id)
            await manager.waitUntilIdle(job.id)
            await manager.cancel(job.id)
        }
        await manager.close()
        #expect(LegacyProtocol.downloadCount == beforeRequests)
        for (job, original, part, result) in originals {
            #expect(try Data(contentsOf: root.appending(path: "\(job.id).json")) == original)
            #expect(FileManager.default.fileExists(atPath: part.path))
            if FileManager.default.fileExists(atPath: part.path) {
                #expect(try Data(contentsOf: part) == Data("keep partial".utf8))
            }
            #expect(try Data(contentsOf: result) == Data("keep result".utf8))
        }
        #expect(try Data(contentsOf: unowned) == Data("unknown retained".utf8))
        // The legacy folder is not added to the new cache's disposable inventory.
        let next = PreviewCache(directory: root.appending(path: "preview-cache-v1"))
        let copy = try await next.reserve(UUID(), bytes: 5)
        #expect(FileManager.default.fileExists(atPath: copy.path))
    }

    @Test @MainActor func ordinaryDownloadsOutsideOldNamespaceStillResumeAndCancel() async throws {
        let root = FileManager.default.temporaryDirectory.appending(path: "cloudnest-nonlegacy-\(UUID())")
        let target = root.appending(path: "previews-backup")
        try FileManager.default.createDirectory(at: target, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let state = await legacySession()
        let disk = TransferDisk()
        let job = TransferRecord(id: UUID(), createdAt: Date(), identity: try #require(state.transferIdentity),
            direction: .download, name: "ordinary.txt", bookmark: try await disk.bookmark(target),
            parentID: 0, fileID: 21, isArchive: false, phase: .paused, totalBytes: 8)
        try JSONEncoder().encode(job).write(to: root.appending(path: "\(job.id).json"))
        let requests = LegacyProtocol.downloadCount
        let manager = TransferManager(session: state, directory: root)
        await manager.restore()
        await manager.resume(job.id)
        await manager.waitUntilIdle(job.id)
        #expect(LegacyProtocol.downloadCount == requests + 1)
        #expect(manager.records[job.id]?.phase == .failed)
        await manager.cancel(job.id)
        #expect(manager.records[job.id]?.phase == .cancelled)
        await manager.close()
    }

    @Test func reservedPathsUseComponentBoundariesAndKeepUnknownSymlinks() throws {
        let root = FileManager.default.temporaryDirectory.appending(path: "cloudnest-legacy-paths-\(UUID())")
        let outside = root.appending(path: "ordinary")
        try FileManager.default.createDirectory(at: outside, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let legacy = LegacyPreviewDirectory(store: root)
        #expect(!legacy.isPresent)
        #expect(legacy.contains(root.appending(path: "previews")))
        #expect(legacy.contains(root.appending(path: "previews/child")))
        #expect(!legacy.contains(root.appending(path: "previews-copy/child")))
        #expect(!legacy.contains(root.appending(path: "preview-cache-v1/child")))
        try FileManager.default.createDirectory(at: legacy.root, withIntermediateDirectories: true)
        #expect(legacy.isPresent)
        let alias = outside.appending(path: "alias")
        try FileManager.default.createSymbolicLink(at: alias, withDestinationURL: legacy.root)
        #expect(legacy.contains(alias))
        // A symlink replacing the root is detected, never traversed for cleanup.
        try FileManager.default.removeItem(at: legacy.root)
        try FileManager.default.createSymbolicLink(at: legacy.root, withDestinationURL: outside)
        let sentinel = outside.appending(path: "keep.txt")
        try Data("retained".utf8).write(to: sentinel)
        #expect(legacy.isPresent)
        #expect(!legacy.contains(outside))
        #expect(try Data(contentsOf: sentinel) == Data("retained".utf8))
    }

    @Test @MainActor func newDownloadsRejectTheReservedOldDestination() async throws {
        let root = FileManager.default.temporaryDirectory.appending(path: "cloudnest-legacy-admission-\(UUID())")
        let old = root.appending(path: "previews/\(UUID())")
        try FileManager.default.createDirectory(at: old, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let state = await legacySession()
        let manager = TransferManager(session: state, directory: root)
        let file = FileItem(id: 21, filename: "remote.txt", parentID: 0, isFolder: 0, size: 8,
                            currentVersion: 1, updateTime: "", fileType: "txt")
        let version = FileVersionItem(id: 3, version: 1, size: 8, comment: "", username: "", createTime: "")
        let requests = LegacyProtocol.downloadCount
        await #expect(throws: TransferError.legacyPreview) {
            try await manager.enqueueDownload(file, destination: old.appending(path: "new.txt"))
        }
        await #expect(throws: TransferError.legacyPreview) {
            try await manager.enqueueVersionDownload(file, version: version, destination: old.appending(path: "version.txt"))
        }
        #expect(manager.records.isEmpty)
        #expect(LegacyProtocol.downloadCount == requests)
        await manager.close()
    }
}
