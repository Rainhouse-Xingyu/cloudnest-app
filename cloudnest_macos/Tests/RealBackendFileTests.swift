import Foundation
import Testing
@testable import CloudNestCore

@Test(.enabled(if: ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_ORIGIN"] != nil &&
               ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_RESTRICTED"] == "0"))
@MainActor func realBackendFileOperations() async throws {
    let env = ProcessInfo.processInfo.environment
    let origin = try ServerOrigin(#require(env["CLOUDNEST_SWIFT_ORIGIN"]))
    let certificateURL = URL(fileURLWithPath: try #require(env["CLOUDNEST_SWIFT_CERTIFICATE"]))
    let delegate = try FixtureTLSDelegate(der: Data(contentsOf: certificateURL))
    let transport = URLSession(configuration: .ephemeral, delegate: delegate, delegateQueue: nil)
    defer { transport.invalidateAndCancel() }
    let api = ServerAPI(session: transport, delegate: delegate)
    let vault = KeychainSessionVault(service: "org.cloudnest.macos.file-test.\(UUID())")
    defer { try? vault.clear() }
    let state = SessionState(api: api, vault: vault)
    await state.restore()
    await state.signIn(server: origin.url.absoluteString,
                       username: try #require(env["CLOUDNEST_SWIFT_USERNAME"]),
                       password: try #require(env["CLOUDNEST_SWIFT_PASSWORD"]))
    let browser = FileBrowserState(session: state, recycleBin: false)
    await browser.reload()
    #expect(browser.error == nil)
    let name = "Native-\(UUID())"
    await browser.createFolder(name: name)
    #expect(browser.error == nil)
    let folder = try #require(browser.items.first { $0.filename == name })
    #expect(folder.isDirectory)
    await browser.open(folder)
    #expect(browser.parentID == folder.id)
    await browser.createFolder(name: "Child")
    #expect(browser.error == nil)
    let child = try #require(browser.items.first { $0.filename == "Child" })
    await browser.createFolder(name: "Child")
    #expect(browser.error == ServerError.conflict.localizedDescription)
    await browser.rename(child, name: "Renamed")
    #expect(browser.error == nil)
    #expect(browser.items.first?.filename == "Renamed")
    #expect(browser.items.first?.modified != nil)

    let otherValue = try #require(env["CLOUDNEST_SWIFT_FORBIDDEN_FILE"])
    let otherID = try #require(Int64(otherValue))
    let result = try await state.trashFiles(ids: [child.id, otherID])
    #expect(result.deletedIDs == [child.id])
    #expect(result.failed.keys.contains(String(otherID)))
    #expect(state.account != nil)
    await browser.reload()
    #expect(browser.items.isEmpty)
    let recycleBin = FileBrowserState(session: state, recycleBin: true)
    await recycleBin.reload()
    #expect(recycleBin.error == nil)
    #expect(recycleBin.items.contains { $0.id == child.id })
    recycleBin.selection = [child.id]
    await recycleBin.restoreSelected()
    #expect(recycleBin.error == nil)
    await browser.reload()
    #expect(browser.items.first?.filename == "Renamed")
    await browser.navigate(to: 0)
    #expect(browser.parentID == 0)
    #expect(browser.items.contains { $0.id == folder.id })
    await state.signOut()
    await browser.reload()
    #expect(browser.items.isEmpty)
    #expect(browser.error == ServerError.unauthorized.localizedDescription)
}
