import Foundation
import Testing
@testable import CloudNestCore

private func commandRoot() throws -> URL {
    let root = FileManager.default.temporaryDirectory.appending(path: "cloudnest-command-store-\(UUID())")
    try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
    return root
}

private func commandIntent() -> FRPCommandIntent {
    FRPCommandIntent(identity: TransferIdentity(server: "https://commands.test", userID: 17), deviceID: 29, action: .stop)
}

@Test func commandIntentAndReceiptSurviveReopenWithoutChangingRequestID() async throws {
    let root = try commandRoot()
    defer { try? FileManager.default.removeItem(at: root) }
    let store = try FRPCommandStore(directory: root)
    let original = commandIntent()
    try await store.insert(original)
    #expect(FRPCommandIntent.validID(original.requestID))
    #expect(throws: FRPCommandStoreError.alreadyRunning) { try FRPCommandStore(directory: root) }
    await store.close()
    let reopened = try FRPCommandStore(directory: root)
    let records = try await reopened.load()
    #expect(records == [original])
    #expect(records.first?.outcome == .unconfirmed)
    var accepted = original
    accepted.revision = 1
    accepted.outcome = .accepted
    accepted.commandID = String(repeating: "a", count: 32)
    try await reopened.replace(accepted)
    await reopened.close()
    let final = try FRPCommandStore(directory: root)
    #expect(try await final.load() == [accepted])
    #expect(accepted.requestID == original.requestID)
    let permissions = try FileManager.default.attributesOfItem(atPath: root.appending(path: "\(original.id).json").path)[.posixPermissions] as? NSNumber
    #expect(permissions?.intValue == 0o600)
    await final.close()
}

@Test func acceptedCommandCannotBeDowngradedOrReboundByALateWrite() async throws {
    let root = try commandRoot()
    defer { try? FileManager.default.removeItem(at: root) }
    let store = try FRPCommandStore(directory: root)
    let original = commandIntent()
    try await store.insert(original)
    var accepted = original
    accepted.revision = 1
    accepted.outcome = .accepted
    accepted.commandID = String(repeating: "b", count: 32)
    try await store.replace(accepted)
    var late = original
    late.revision = 2
    late.error = "Result unconfirmed"
    await #expect(throws: FRPCommandStoreError.conflict) { try await store.replace(late) }
    var differentReceipt = accepted
    differentReceipt.revision = 2
    differentReceipt.commandID = String(repeating: "c", count: 32)
    await #expect(throws: FRPCommandStoreError.conflict) { try await store.replace(differentReceipt) }
    var fields = try #require(JSONSerialization.jsonObject(with: JSONEncoder().encode(accepted)) as? [String: Any])
    fields["deviceID"] = 30
    fields["revision"] = 2
    let rebound = try JSONDecoder().decode(FRPCommandIntent.self, from: JSONSerialization.data(withJSONObject: fields))
    await #expect(throws: FRPCommandStoreError.conflict) { try await store.replace(rebound) }
    #expect(try await store.load() == [accepted])
    await store.close()
}

@Test func commandStoreKeepsWritingTheLockedDirectoryAfterPathReplacement() async throws {
    let root = try commandRoot()
    defer { try? FileManager.default.removeItem(at: root) }
    let current = root.appending(path: "current")
    let retained = root.appending(path: "retained")
    let store = try FRPCommandStore(directory: current)
    var record = commandIntent()
    try await store.insert(record)
    try FileManager.default.moveItem(at: current, to: retained)
    try FileManager.default.createDirectory(at: current, withIntermediateDirectories: true)
    let sentinel = current.appending(path: "user-file.txt")
    let bytes = Data("Unrelated directory content".utf8)
    try bytes.write(to: sentinel)
    record.revision = 1
    record.error = "Connection lost; retry the same request"
    try await store.replace(record)
    await store.close()
    let restored = try FRPCommandStore(directory: retained)
    #expect(try await restored.load() == [record])
    #expect(try Data(contentsOf: sentinel) == bytes)
    #expect(!FileManager.default.fileExists(atPath: current.appending(path: "\(record.id).json").path))
    await restored.close()
}

@Test func commandStoreRejectsSymlinkAndInvalidRecordsWithoutAlteringThem() async throws {
    let root = try commandRoot()
    defer { try? FileManager.default.removeItem(at: root) }
    let folder = root.appending(path: "journal")
    try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
    let original = commandIntent()
    let outside = root.appending(path: "outside.json")
    let bytes = try JSONEncoder().encode(original)
    try bytes.write(to: outside)
    let recordURL = folder.appending(path: "\(original.id).json")
    try FileManager.default.createSymbolicLink(at: recordURL, withDestinationURL: outside)
    let linked = try FRPCommandStore(directory: folder)
    await #expect(throws: FRPCommandStoreError.invalidJournal) { try await linked.load() }
    await linked.close()
    #expect(try Data(contentsOf: outside) == bytes)
    try FileManager.default.removeItem(at: recordURL)
    let invalid = Data("{\"partial\":true}".utf8)
    try invalid.write(to: recordURL)
    let partial = try FRPCommandStore(directory: folder)
    await #expect(throws: FRPCommandStoreError.invalidJournal) { try await partial.load() }
    await partial.close()
    #expect(try Data(contentsOf: recordURL) == invalid)
}

@Test func preservedCrashFilesCountAgainstCommandJournalCapacity() async throws {
    let root = try commandRoot()
    defer { try? FileManager.default.removeItem(at: root) }
    for _ in 0..<1024 {
        try Data().write(to: root.appending(path: ".\(UUID()).tmp"))
    }
    let store = try FRPCommandStore(directory: root)
    #expect(try await store.load().isEmpty)
    await #expect(throws: FRPCommandStoreError.full) { try await store.insert(commandIntent()) }
    await store.close()
    #expect(try FileManager.default.contentsOfDirectory(at: root, includingPropertiesForKeys: nil).filter { $0.pathExtension == "tmp" }.count == 1024)
}
