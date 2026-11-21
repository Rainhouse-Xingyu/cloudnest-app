import Foundation
import CryptoKit
import Darwin
import Testing
@testable import CloudNestCore

private final class LargeFileVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ session: SavedSession) throws { saved = session }
    func clear() throws { saved = nil }
}

private struct LargeFileState: Codable {
    let bytes: Int64
    let sha256: String
    var upload: UUID?
    var download: UUID?
    var fileID: Int64?
    var downloadConfirmed: Int64?
    var etag: String?
}

private struct LargeFilePhase: Codable {
    let phase: String
    let pid: Int32
    let bytes: Int64
    let confirmedBytes: Int64
    let elapsedSeconds: Double
    let baselineRSSBytes: Int64
    let peakRSSBytes: Int64
    let sha256: String
}

private enum LargeFileFailure: Error { case invalidPhase, timeout, failed(String), resource }

private func peakRSS() throws -> Int64 {
    var usage = rusage()
    guard getrusage(RUSAGE_SELF, &usage) == 0 else { throw LargeFileFailure.resource }
    // Darwin ru_maxrss is in bytes. Linux uses a different unit.
    return Int64(usage.ru_maxrss)
}

private func createLargeFile(_ url: URL, bytes: Int64) throws -> String {
    guard !FileManager.default.fileExists(atPath: url.path),
          FileManager.default.createFile(atPath: url.path, contents: nil, attributes: [.posixPermissions: 0o600]) else {
        throw LargeFileFailure.resource
    }
    let handle = try FileHandle(forWritingTo: url)
    defer { try? handle.close() }
    let template = Data((0..<(1024 * 1024)).map { UInt8(truncatingIfNeeded: $0) })
    var digest = SHA256()
    var remaining = bytes
    var index: UInt64 = 0
    while remaining > 0 {
        let count = Int(min(Int64(template.count), remaining))
        try autoreleasepool {
            var block = template
            block.withUnsafeMutableBytes { $0.storeBytes(of: index.littleEndian, as: UInt64.self) }
            let part = block.prefix(count)
            try handle.write(contentsOf: part)
            digest.update(data: part)
        }
        remaining -= Int64(count)
        index += 1
    }
    try handle.synchronize()
    return TransferDisk.hex(digest.finalize())
}

private func largeFileHash(_ url: URL) throws -> String {
    let handle = try FileHandle(forReadingFrom: url)
    defer { try? handle.close() }
    var digest = SHA256()
    while true {
        let count = try autoreleasepool { () -> Int in
            let data = try handle.read(upToCount: 1024 * 1024) ?? Data()
            digest.update(data: data)
            return data.count
        }
        if count == 0 { break }
    }
    return TransferDisk.hex(digest.finalize())
}

@MainActor private func waitForLargeCheckpoint(_ manager: TransferManager, id: UUID, half: Int64) async throws {
    let deadline = ContinuousClock.now.advanced(by: .seconds(600))
    while ContinuousClock.now < deadline {
        let job = try #require(manager.records[id])
        if job.phase == .running && job.confirmedBytes >= half { return }
        if job.phase == .failed || job.phase == .completed {
            throw LargeFileFailure.failed(job.error ?? "transfer finished before the pause checkpoint")
        }
        try await Task.sleep(for: .milliseconds(5))
    }
    throw LargeFileFailure.timeout
}

// Opt-in only: four separate Swift processes use the same private journal.
// Normal CI compiles this test, but never performs a GiB transfer implicitly.
@Test(.enabled(if: ProcessInfo.processInfo.environment["CLOUDNEST_NATIVE_LARGE_FILE"] == "1"))
@MainActor func nativeLargeFileTransferAcceptance() async throws {
    let env = ProcessInfo.processInfo.environment
    let phase = try #require(env["CLOUDNEST_NATIVE_LARGE_FILE_PHASE"])
    let root = URL(fileURLWithPath: try #require(env["CLOUDNEST_NATIVE_LARGE_FILE_DIRECTORY"]))
    let source = root.appending(path: "large-source.bin")
    let stateURL = root.appending(path: "state.json")
    let origin = try ServerOrigin(#require(env["CLOUDNEST_SWIFT_ORIGIN"]))
    let policy = try FixtureTLSDelegate(der: Data(contentsOf: URL(fileURLWithPath: #require(env["CLOUDNEST_SWIFT_CERTIFICATE"]))))
    let configuration = URLSessionConfiguration.ephemeral
    configuration.timeoutIntervalForResource = 600
    configuration.httpAdditionalHeaders = ["X-CloudNest-Test-Large-File-Phase": phase]
    let transport = URLSession(configuration: configuration, delegate: policy, delegateQueue: nil)
    defer { transport.invalidateAndCancel() }
    let session = SessionState(api: ServerAPI(session: transport, delegate: policy), vault: LargeFileVault())
    await session.restore()
    await session.signIn(server: origin.url.absoluteString, username: try #require(env["CLOUDNEST_SWIFT_USERNAME"]),
                         password: try #require(env["CLOUDNEST_SWIFT_PASSWORD"]))
    let identity = try #require(session.transferIdentity)
    let baselineRSS = try peakRSS()
    var state: LargeFileState
    if phase == "upload-pause" {
        let bytes: Int64 = (1 << 30) + 173
        state = LargeFileState(bytes: bytes, sha256: try createLargeFile(source, bytes: bytes))
    } else {
        state = try JSONDecoder().decode(LargeFileState.self, from: Data(contentsOf: stateURL))
    }
    let manager = TransferManager(session: session, directory: root.appending(path: "journal"))
    await manager.restore()
    let start = ContinuousClock.now
    let id: UUID
    switch phase {
    case "upload-pause":
        id = try await manager.enqueueUpload(source, parentID: 0, expectedIdentity: identity)
        state.upload = id
        try await waitForLargeCheckpoint(manager, id: id, half: state.bytes / 2)
        await manager.pause(id)
        #expect(manager.records[id]?.phase == .paused)
        #expect((manager.records[id]?.confirmedBytes ?? 0) < state.bytes)
    case "upload-resume":
        id = try #require(state.upload)
        #expect(manager.records[id]?.phase == .paused)
        #expect((manager.records[id]?.confirmedBytes ?? 0) >= state.bytes / 2)
        #expect(manager.records[id]?.source?.sha256 == state.sha256)
        await manager.resume(id)
        await manager.waitUntilIdle(id)
        #expect(manager.records[id]?.phase == .completed)
        #expect(manager.records[id]?.confirmedBytes == state.bytes)
        state.fileID = try #require(manager.records[id]?.resultFileID)
    case "download-pause":
        let fileID = try #require(state.fileID)
        let file = try #require(try await session.listFiles(parentID: 0, recycleBin: false).first { $0.id == fileID })
        #expect(file.size == state.bytes)
        id = try await manager.enqueueDownload(file, destination: root.appending(path: "large-downloaded.bin"), expectedIdentity: identity)
        state.download = id
        try await waitForLargeCheckpoint(manager, id: id, half: state.bytes / 2)
        await manager.pause(id)
        #expect(manager.records[id]?.phase == .paused)
        state.downloadConfirmed = manager.records[id]?.confirmedBytes
        state.etag = try #require(manager.records[id]?.etag)
        #expect((state.downloadConfirmed ?? 0) < state.bytes)
    case "download-resume":
        id = try #require(state.download)
        #expect(manager.records[id]?.phase == .paused)
        #expect(manager.records[id]?.confirmedBytes == state.downloadConfirmed)
        #expect(manager.records[id]?.etag == state.etag)
        await manager.resume(id)
        await manager.waitUntilIdle(id)
        #expect(manager.records[id]?.phase == .completed)
        #expect(manager.records[id]?.confirmedBytes == state.bytes)
        let result = try await manager.resultURL(id)
        #expect(try largeFileHash(result) == state.sha256)
        #expect(!FileManager.default.fileExists(atPath: root.appending(path: ".cloudnest-\(id).part").path))
    default: throw LargeFileFailure.invalidPhase
    }
    let confirmed = try #require(manager.records[id]?.confirmedBytes)
    await manager.close()
    if phase == "download-pause" {
        let part = root.appending(path: ".cloudnest-\(id).part")
        let handle = try FileHandle(forWritingTo: part)
        try handle.seekToEnd()
        try handle.write(contentsOf: Data("unconfirmed suffix from the previous process".utf8))
        try handle.synchronize()
        try handle.close()
    }
    try JSONEncoder().encode(state).write(to: stateURL, options: .atomic)
    let duration = start.duration(to: .now).components
    let elapsed = Double(duration.seconds) + Double(duration.attoseconds) / 1e18
    let rss = try peakRSS()
    #expect(rss > 0 && rss < 512 * 1024 * 1024)
    let report = LargeFilePhase(phase: phase, pid: ProcessInfo.processInfo.processIdentifier, bytes: state.bytes,
        confirmedBytes: confirmed, elapsedSeconds: elapsed, baselineRSSBytes: baselineRSS,
        peakRSSBytes: rss, sha256: state.sha256)
    try JSONEncoder().encode(report).write(to: root.appending(path: "\(phase).json"), options: .atomic)
    await session.signOut()
}
