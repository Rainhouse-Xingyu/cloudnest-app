import Foundation
import CryptoKit
import Testing
@testable import CloudNestCore

private func transferFixture(name: String = "out.bin", total: Int64 = 0) -> TransferRecord {
    TransferRecord(id: UUID(), createdAt: Date(), identity: TransferIdentity(server: "https://example.test", userID: 7),
                   direction: .download, name: name, bookmark: Data(), parentID: 0, fileID: 21,
                   isArchive: false, totalBytes: total)
}

@Test func downloadPlansRequireStrongIdentityAndResetOnFullResponse() throws {
    let data = Data("replacement".utf8)
    let hash = SHA256.hash(data: data)
    let tag = "\"sha256-\(TransferDisk.hex(hash))\""
    var headers = ["ETag": tag, "Repr-Digest": "sha-256=:\(Data(hash).base64EncodedString()):",
                   "Content-Length": String(data.count)]
    var job = transferFixture(total: 100)
    job.confirmedBytes = 8
    job.etag = "\"sha256-\(String(repeating: "0", count: 64))\""
    let full = HTTPURLResponse(url: URL(string: "https://example.test")!, statusCode: 200,
                               httpVersion: nil, headerFields: headers)!
    let replacement = try DownloadPlan(response: full, record: job, requestedStart: 8, requestedEnd: 99)
    #expect(replacement.replaces)
    #expect(replacement.total == Int64(data.count))
    headers["Content-Range"] = "bytes 8-18/100"
    let partial = HTTPURLResponse(url: full.url!, statusCode: 206, httpVersion: nil, headerFields: headers)!
    #expect(throws: TransferError.invalidDownload) {
        try DownloadPlan(response: partial, record: job, requestedStart: 8, requestedEnd: 99)
    }
    headers["ETag"] = "W/" + tag
    let weak = HTTPURLResponse(url: full.url!, statusCode: 200, httpVersion: nil, headerFields: headers)!
    #expect(throws: TransferError.invalidDownload) {
        try DownloadPlan(response: weak, record: job, requestedStart: 0, requestedEnd: 99)
    }
}

@Test func uploadManifestRejectsChangedSourceAndSupportsEmptyFiles() async throws {
    let directory = FileManager.default.temporaryDirectory.appending(path: "cloudnest-source-\(UUID())")
    try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    defer { try? FileManager.default.removeItem(at: directory) }
    let url = directory.appending(path: "source.bin")
    try Data("original".utf8).write(to: url)
    let disk = TransferDisk()
    let source = try await disk.prepareUpload(url)
    #expect(source.size == 8)
    #expect(source.chunks.count == 1)
    try Data("modified".utf8).write(to: url)
    await #expect(throws: TransferError.sourceChanged) { try await disk.uploadChunk(url, source: source, index: 0) }
    try Data().write(to: url)
    let empty = try await disk.prepareUpload(url)
    #expect(empty.size == 0)
    #expect(empty.chunks.isEmpty)
    #expect(empty.sha256 == TransferDisk.hex(SHA256.hash(data: Data())))
}

@Test func openEndedDownloadRequiresTheWholeRemainingRepresentation() throws {
    let data = Data(repeating: 0x43, count: 100)
    let digest = SHA256.hash(data: data)
    let tag = "\"sha256-\(TransferDisk.hex(digest))\""
    var job = transferFixture(total: 100)
    job.confirmedBytes = 40
    job.etag = tag
    var headers = ["ETag": tag, "Repr-Digest": "sha-256=:\(Data(digest).base64EncodedString()):",
                   "Content-Length": "60", "Content-Range": "bytes 40-99/100"]
    func response() throws -> HTTPURLResponse {
        try #require(HTTPURLResponse(url: URL(string: "https://example.test")!, statusCode: 206,
                                     httpVersion: nil, headerFields: headers))
    }
    let plan = try DownloadPlan(response: response(), record: job, requestedStart: 40, requestedEnd: .max)
    #expect(plan.total == 100)
    #expect(plan.length == 60)
    headers["Content-Length"] = "40"
    headers["Content-Range"] = "bytes 40-79/100"
    #expect(throws: TransferError.invalidDownload) {
        try DownloadPlan(response: response(), record: job, requestedStart: 40, requestedEnd: .max)
    }
}

@Test func downloadCheckpointRejectsTamperingAndPreservesExistingDestination() async throws {
    let directory = FileManager.default.temporaryDirectory.appending(path: "cloudnest-target-\(UUID())")
    try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    defer { try? FileManager.default.removeItem(at: directory) }
    let data = Data("verified bytes".utf8)
    var job = transferFixture(total: Int64(data.count))
    let disk = TransferDisk()
    try await disk.openDownload(job, directory: directory)
    try await disk.appendDownload(job.id, data: data, offset: 0)
    try await disk.synchronizeDownload(job.id)
    job.confirmedBytes = Int64(data.count)
    job.downloadedChunks = [TransferDisk.hex(SHA256.hash(data: data))]
    job.sha256 = job.downloadedChunks.first
    job.resultName = job.name
    let target = directory.appending(path: job.name)
    try Data("keep existing".utf8).write(to: target)
    await #expect(throws: TransferError.destinationExists) { try await disk.publishDownload(job, directory: directory) }
    #expect(try Data(contentsOf: target) == Data("keep existing".utf8))
    job.resultName = TransferDisk.availableName(job.name, suffix: 1)
    let name = try await disk.publishDownload(job, directory: directory)
    #expect(name == "out (1).bin")
    // The persisted publication intent reuses the same inode after a restart.
    await disk.closeDownload(job.id)
    try await disk.openDownload(job, directory: directory)
    #expect(try await disk.publishDownload(job, directory: directory) == name)
    await disk.closeDownload(job.id)
    let part = directory.appending(path: ".cloudnest-\(job.id).part")
    try Data(repeating: 0, count: data.count).write(to: part)
    await #expect(throws: TransferError.invalidCheckpoint) { try await disk.openDownload(job, directory: directory) }
}

@Test func transferJournalLocksAndPersistsAccountBoundRecords() async throws {
    let directory = FileManager.default.temporaryDirectory.appending(path: "cloudnest-journal-\(UUID())")
    defer { try? FileManager.default.removeItem(at: directory) }
    let store = try TransferStore(directory: directory)
    let job = transferFixture()
    try await store.save(job)
    let recovered = try await store.load()
    #expect(recovered.count == 1)
    #expect(recovered.first?.identity == job.identity)
    #expect(throws: TransferError.alreadyRunning) { try TransferStore(directory: directory) }
    let data = try Data(contentsOf: directory.appending(path: "\(job.id).json"))
    let json = try #require(JSONSerialization.jsonObject(with: data) as? [String: Any])
    #expect(json["token"] == nil)
}

private final class StreamFixtureProtocol: URLProtocol, @unchecked Sendable {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil,
                                       headerFields: ["Content-Length": "65536"])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(repeating: 7, count: 65536))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

private final class StreamFixturePolicy: NSObject, URLSessionTaskDelegate {}

@Test func cancellingDownloadReleasesAnUnacknowledgedBlock() async throws {
    let configuration = URLSessionConfiguration.ephemeral
    configuration.protocolClasses = [StreamFixtureProtocol.self]
    let stream = DownloadStream(configuration: configuration, policy: StreamFixturePolicy())
    defer { stream.cancel() }
    let response = try await stream.start(URLRequest(url: URL(string: "https://stream.test/file")!))
    #expect(response.statusCode == 200)
    var iterator = stream.chunks.makeAsyncIterator()
    let chunk = try #require(try await iterator.next())
    #expect(!chunk.data.isEmpty)
    stream.cancel()
    // Cancel must release the delegate even when the consumer still owns a block.
    do {
        _ = try await iterator.next()
        Issue.record("The cancelled stream completed without its cancellation error")
    } catch { #expect(error is CancellationError) }
}
