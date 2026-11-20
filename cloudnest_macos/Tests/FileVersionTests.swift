import Foundation
import CryptoKit
import Testing
@testable import CloudNestCore

private final class VersionResponseProtocol: URLProtocol, @unchecked Sendable {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        guard let url = request.url else { return }
        let item = #"{"id":1,"version":1,"size":8,"comment":"fixture","username":"reader","create_time":"2026-11-20T00:00:00Z"}"#
        var fileID = 7
        var items = item
        switch url.host {
        case "wrong-file.test": fileID = 8
        case "duplicate.test": items = item + "," + item
        case "future-version.test": items = item.replacingOccurrences(of: #""version":1"#, with: #""version":3"#)
        case "negative-size.test": items = item.replacingOccurrences(of: #""size":8"#, with: #""size":-1"#)
        default: break
        }
        let status = request.httpMethod == "GET" && request.value(forHTTPHeaderField: "Authorization") == "Bearer fixture-token" ? 200 : 400
        let body = "{\"file_id\":\(fileID),\"current_version\":2,\"items\":[\(items)]}"
        let response = HTTPURLResponse(url: url, statusCode: status, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(body.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

@Test func versionResponsesValidateFileBindingAndVersionRows() async throws {
    let configuration = URLSessionConfiguration.ephemeral
    configuration.protocolClasses = [VersionResponseProtocol.self]
    let api = ServerAPI(configuration: configuration)
    let valid = try await api.fileVersions(ServerOrigin("https://valid.test"), token: "fixture-token", fileID: 7)
    #expect(valid.currentVersion == 2)
    #expect(valid.items.first?.version == 1)
    for host in ["wrong-file.test", "duplicate.test", "future-version.test", "negative-size.test"] {
        do {
            _ = try await api.fileVersions(ServerOrigin("https://\(host)"), token: "fixture-token", fileID: 7)
            Issue.record("An inconsistent version response was accepted")
        } catch { #expect(error as? ServerError == .invalidResponse) }
    }
}

@Test func historicalDownloadPlansPinTheirSizeAndFirstIdentity() throws {
    func response(_ data: Data) throws -> HTTPURLResponse {
        let hash = SHA256.hash(data: data)
        return try #require(HTTPURLResponse(url: URL(string: "https://example.test/versions/1/download")!, statusCode: 200,
            httpVersion: nil, headerFields: ["ETag": "\"sha256-\(TransferDisk.hex(hash))\"",
            "Repr-Digest": "sha-256=:\(Data(hash).base64EncodedString()):", "Content-Length": String(data.count)]))
    }
    let original = Data("original".utf8)
    let originalResponse = try response(original)
    var job = TransferRecord(id: UUID(), createdAt: Date(), identity: TransferIdentity(server: "https://example.test", userID: 7),
                             direction: .versionDownload, name: "history.bin", bookmark: Data(), parentID: 0, fileID: 23,
                             isArchive: false, totalBytes: Int64(original.count))
    job.fileVersion = 1
    job.versionSize = Int64(original.count)
    _ = try DownloadPlan(response: originalResponse, record: job, requestedStart: 0, requestedEnd: 1024)
    #expect(throws: TransferError.invalidDownload) {
        try DownloadPlan(response: response(Data("different size".utf8)), record: job, requestedStart: 0, requestedEnd: 1024)
    }
    job.etag = originalResponse.value(forHTTPHeaderField: "ETag")
    #expect(throws: TransferError.invalidDownload) {
        try DownloadPlan(response: response(Data("modified".utf8)), record: job, requestedStart: 0, requestedEnd: 1024)
    }
    job.versionSize = nil
    #expect(throws: TransferError.invalidDownload) {
        try DownloadPlan(response: response(Data("modified".utf8)), record: job, requestedStart: 0, requestedEnd: 1024)
    }
}

@Test func versionCheckpointsRetainTheirSelectorAndRejectInvalidCombinations() async throws {
    let root = FileManager.default.temporaryDirectory.appending(path: "cloudnest-version-journal-\(UUID())")
    defer { try? FileManager.default.removeItem(at: root) }
    let store = try TransferStore(directory: root)
    let legacy = TransferRecord(id: UUID(), createdAt: Date(), identity: TransferIdentity(server: "https://example.test", userID: 7),
                                direction: .download, name: "current.bin", bookmark: Data(), parentID: 0, fileID: 23,
                                isArchive: false, totalBytes: 512)
    try await store.save(legacy)
    #expect(try await store.load().first?.fileVersion == nil)
    var history = TransferRecord(id: legacy.id, createdAt: legacy.createdAt, identity: legacy.identity,
                                 direction: .versionDownload, name: legacy.name, bookmark: legacy.bookmark,
                                 parentID: 0, fileID: legacy.fileID, isArchive: false, totalBytes: 512)
    history.fileVersion = 2
    history.versionSize = 512
    try await store.save(history)
    let loaded = try #require(try await store.load().first)
    #expect(loaded.fileVersion == 2)
    #expect(loaded.versionSize == 512)
    enum PreviousDirection: String, Decodable { case upload, download }
    struct PreviousRecord: Decodable { let direction: PreviousDirection }
    let oldFormat = try JSONEncoder().encode(legacy)
    #expect(try JSONDecoder().decode(PreviousRecord.self, from: oldFormat).direction == .download)
    let newFormat = try JSONEncoder().encode(history)
    #expect(throws: DecodingError.self) { try JSONDecoder().decode(PreviousRecord.self, from: newFormat) }
    for (version, size) in [(0, 512), (-1, 512), (2, -1), (2, 511)] {
        var bad = history
        bad.fileVersion = version
        bad.versionSize = Int64(size)
        try await store.save(bad)
        await #expect(throws: TransferError.invalidCheckpoint) { try await store.load() }
    }
    history.fileVersion = nil
    try await store.save(history)
    await #expect(throws: TransferError.invalidCheckpoint) { try await store.load() }
}
