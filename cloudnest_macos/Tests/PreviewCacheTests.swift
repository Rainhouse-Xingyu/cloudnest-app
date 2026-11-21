import Foundation
import Testing
@testable import CloudNestCore

@Test func previewsReserveBeforeWritingAndKeepOpenCopies() async throws {
    let root = FileManager.default.temporaryDirectory.appending(path: "cloudnest-preview-budget-\(UUID())")
    defer { try? FileManager.default.removeItem(at: root) }
    let cache = PreviewCache(directory: root, limits: .init(bytes: 10, fileBytes: 8, entries: 2))
    let first = UUID(), second = UUID(), next = UUID()
    let firstFolder = try await cache.reserve(first, bytes: 6)
    let openCopy = firstFolder.appending(path: "open.txt")
    try Data("pinned".utf8).write(to: openCopy)
    await #expect(throws: TransferError.previewCapacity) { try await cache.reserve(second, bytes: 5) }
    #expect(try Data(contentsOf: openCopy) == Data("pinned".utf8))
    _ = try await cache.reserve(second, bytes: 4)
    await #expect(throws: TransferError.previewCapacity) { try await cache.reserve(next, bytes: 0) }
    await #expect(throws: TransferError.previewTooLarge) { try await cache.validate(first, total: 7) }
    try await cache.validate(first, total: 6)
    try await cache.remove(second)
    #expect(FileManager.default.fileExists(atPath: openCopy.path))
    try await cache.remove(first)
    #expect(!FileManager.default.fileExists(atPath: firstFolder.path))
    await #expect(throws: TransferError.previewTooLarge) { try await cache.reserve(next, bytes: 9) }
    _ = try await cache.reserve(next, bytes: 8)
}

@Test func previewRecoveryRemovesOwnedCrashCopiesAndRetainsUnknownEntries() async throws {
    let parent = FileManager.default.temporaryDirectory.appending(path: "cloudnest-preview-recovery-\(UUID())")
    defer { try? FileManager.default.removeItem(at: parent) }
    let root = parent.appending(path: "cache")
    let cache = PreviewCache(directory: root)
    let folder = try await cache.reserve(UUID(), bytes: 5)
    try Data("crash".utf8).write(to: folder.appending(path: "partial"))
    let reconstructed = PreviewCache(directory: root)
    try await reconstructed.recover()
    #expect(!FileManager.default.fileExists(atPath: folder.path))
    // Unknown files must never be silently included in disposable UUID data.
    let unknown = root.appending(path: "keep.txt")
    try Data("retain".utf8).write(to: unknown)
    let blocked = PreviewCache(directory: root)
    await #expect(throws: TransferError.previewStorage) { try await blocked.recover() }
    #expect(try Data(contentsOf: unknown) == Data("retain".utf8))
}

@Test func previewRecoveryDoesNotFollowSymlinkDirectories() async throws {
    let parent = FileManager.default.temporaryDirectory.appending(path: "cloudnest-preview-symlink-\(UUID())")
    defer { try? FileManager.default.removeItem(at: parent) }
    let root = parent.appending(path: "cache")
    let outside = parent.appending(path: "user-files")
    try FileManager.default.createDirectory(at: outside, withIntermediateDirectories: true)
    let original = outside.appending(path: "keep.txt")
    try Data("user file".utf8).write(to: original)
    let cache = PreviewCache(directory: root)
    try await cache.recover()
    try FileManager.default.createSymbolicLink(at: root.appending(path: UUID().uuidString), withDestinationURL: outside)
    let reconstructed = PreviewCache(directory: root)
    await #expect(throws: TransferError.previewStorage) { try await reconstructed.recover() }
    #expect(try Data(contentsOf: original) == Data("user file".utf8))
    let linkedRoot = parent.appending(path: "linked-cache")
    try FileManager.default.createSymbolicLink(at: linkedRoot, withDestinationURL: outside)
    let unsafe = PreviewCache(directory: linkedRoot)
    await #expect(throws: TransferError.previewStorage) { try await unsafe.recover() }
    #expect(try Data(contentsOf: original) == Data("user file".utf8))
}
