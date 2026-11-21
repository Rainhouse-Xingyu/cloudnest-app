import Foundation
import Testing
@testable import CloudNestCore

@Test func versionUploadJournalsFreezeTheTargetAndRejectAnOlderClient() async throws {
    let root = FileManager.default.temporaryDirectory.appending(path: "cloudnest-version-upload-journal-\(UUID())")
    defer { try? FileManager.default.removeItem(at: root) }
    let store = try TransferStore(directory: root)
    var job = TransferRecord(id: UUID(), createdAt: Date(), identity: .init(server: "https://example.test", userID: 7),
        direction: .versionUpload, name: "source.bin", bookmark: Data(), parentID: 0, fileID: 23, isArchive: false, totalBytes: 0)
    job.baseVersion = 2
    job.versionComment = "Confirmed version two"
    job.versionTargetName = "original.bin"
    try await store.save(job)
    let restored = try #require(try await store.load().first)
    #expect(restored.direction == .versionUpload)
    #expect(restored.direction.isUpload)
    #expect(!restored.direction.isDownload)
    #expect(restored.fileID == 23)
    #expect(restored.baseVersion == 2)
    #expect(restored.versionComment == job.versionComment)
    enum OlderDirection: String, Decodable { case upload, download, versionDownload }
    struct OlderRecord: Decodable { let direction: OlderDirection }
    let encoded = try JSONEncoder().encode(job)
    #expect(throws: DecodingError.self) { try JSONDecoder().decode(OlderRecord.self, from: encoded) }
    var invalid = job
    invalid.baseVersion = nil
    try await store.save(invalid)
    await #expect(throws: TransferError.invalidCheckpoint) { try await store.load() }
    invalid = job
    invalid.publishedVersion = 4
    try await store.save(invalid)
    await #expect(throws: TransferError.invalidCheckpoint) { try await store.load() }
    invalid = job
    invalid.versionComment = String(repeating: "x\u{301}", count: 300)
    try await store.save(invalid)
    await #expect(throws: TransferError.invalidCheckpoint) { try await store.load() }
    invalid = job
    invalid.versionComment = String(repeating: "界", count: 256)
    try await store.save(invalid)
    await #expect(throws: TransferError.invalidCheckpoint) { try await store.load() }
    invalid.versionComment = String(repeating: "界", count: 255)
    try await store.save(invalid)
    #expect(try await store.load().first?.versionComment == invalid.versionComment)
    invalid = job
    invalid.phase = .completed
    invalid.publishedVersion = 3
    invalid.resultFileID = 24
    try await store.save(invalid)
    await #expect(throws: TransferError.invalidCheckpoint) { try await store.load() }
    job.phase = .completed
    job.publishedVersion = 3
    job.resultFileID = 23
    try await store.save(job)
    #expect(try await store.load().first?.publishedVersion == 3)
}
