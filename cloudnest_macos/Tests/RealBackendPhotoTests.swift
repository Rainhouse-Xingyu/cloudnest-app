import Foundation
import CryptoKit
import ImageIO
import Testing
@testable import CloudNestCore

private struct NativePhotoFixture: Decodable {
    let origin: String; let certificate: String; let username: String; let password: String
    let role: String; let user_id: Int64; let owner_id: Int64
    let image_id: Int64; let video_id: Int64; let static_id: Int64; let private_id: Int64; let pair_id: Int64
    let image_sha256: String; let video_sha256: String; let static_sha256: String
    let generator: String; let directory: String
    let cancel_marker: String
    let pair: LivePhotoPair?
}
private final class NativePhotoVault: SessionVault {
    private var saved: SavedSession?
    func read() throws -> SavedSession? { saved }
    func write(_ value: SavedSession) throws { saved = value }
    func clear() throws { saved = nil }
}

extension RealBackendTests {
    @Test(.enabled(if:ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_PHOTO_FIXTURE"] != nil))
    @MainActor func realBackendPhotos() async throws {
        let path = try #require(ProcessInfo.processInfo.environment["CLOUDNEST_SWIFT_PHOTO_FIXTURE"])
        let f = try JSONDecoder().decode(NativePhotoFixture.self,from:Data(contentsOf:URL(fileURLWithPath:path)))
        let delegate = try FixtureTLSDelegate(der:Data(contentsOf:URL(fileURLWithPath:f.certificate)))
        let transport = URLSession(configuration:.ephemeral,delegate:delegate,delegateQueue:nil)
        defer { transport.invalidateAndCancel() }
        let api = ServerAPI(session:transport,delegate:delegate)
        let session = SessionState(api:api,vault:NativePhotoVault())
        await session.restore(); await session.signIn(server:f.origin,username:f.username,password:f.password)
        let identity = try #require(session.transferIdentity)
        #expect(identity.userID == f.user_id)
        let gallery = PhotoState(session:session)
        await gallery.load(try PhotoQuery())
        if f.role == "restricted" {
            #expect(gallery.page == nil && gallery.error == ServerError.forbidden.localizedDescription && session.account != nil)
            await #expect(throws:ServerError.forbidden) { try await session.livePhotos(page:1) }
            return
        }
        let page = try #require(gallery.page)
        let expected: Set<Int64> = ["owner","cancelled"].contains(f.role) ? [f.image_id,f.static_id] : [f.image_id,f.private_id]
        #expect(Set(page.items.map(\.id)) == expected && gallery.thumbnails.count == 2 && gallery.thumbnailErrors.isEmpty)
        for data in gallery.thumbnails.values {
            let source = try #require(CGImageSourceCreateWithData(data as CFData,nil))
            let properties = try #require(CGImageSourceCopyPropertiesAtIndex(source,0,nil) as? [CFString:Any])
            #expect((properties[kCGImagePropertyPixelWidth] as? NSNumber)?.intValue == 256)
            #expect((properties[kCGImagePropertyPixelHeight] as? NSNumber)?.intValue == 192)
        }
        let root = URL(fileURLWithPath:f.directory)
        let transfers = TransferManager(session:session,directory:root.appending(path:"journal"))
        await transfers.restore()
        let preview = MediaPreviewState(session:session,transfers:transfers)
        func check(_ url: URL, hash: String) throws {
            #expect(try TransferDisk.hex(SHA256.hash(data:Data(contentsOf:url))) == hash)
        }
        func decode(_ urls: [URL]) throws {
            let process = Process(); process.executableURL = URL(fileURLWithPath:f.generator)
            process.arguments = ["--verify"] + urls.map(\.path)
            let output = Pipe();process.standardOutput = output;process.standardError = output
            try process.run(); process.waitUntilExit()
            let data = output.fileHandleForReading.readDataToEndOfFile()
            #expect(process.terminationStatus == 0)
            #expect(String(decoding:data,as:UTF8.self).contains("\"success\":true"))
        }
        if f.role == "cancelled" {
            let pair = try #require(f.pair)
            let task = Task { await preview.load(.livePhoto(pair)) }
            let deadline = ContinuousClock.now.advanced(by:.seconds(8))
            while !FileManager.default.fileExists(atPath:f.cancel_marker) {
                guard ContinuousClock.now < deadline else { await preview.close(); await task.value; throw ServerError.unavailable }
                try await Task.sleep(for:.milliseconds(10))
            }
            #expect(preview.busy && preview.error == nil)
            let start = ContinuousClock.now
            await preview.close(); await task.value
            #expect(start.duration(to:.now) < .seconds(3))
            #expect(preview.urls.isEmpty && !preview.busy && preview.error == nil)
        } else if f.role == "owner" {
            #expect(try await session.livePhotos(page:1).items.isEmpty)
            let still = try #require(page.items.first { $0.id == f.image_id })
            let png = try #require(page.items.first { $0.id == f.static_id })
            for (file,hash) in [(still,f.image_sha256),(png,f.static_sha256)] {
                await preview.load(.photo(file)); #expect(preview.error == nil)
                try check(try #require(preview.urls.first),hash:hash)
                #expect(preview.urls.count == 1)
                await preview.close()
            }
            let binding = LivePhotoBindingState(session:session,image:still)
            await binding.load(try PhotoQuery())
            let video = try #require(binding.page?.items.first { $0.id == f.video_id })
            await binding.bind(video)
            let pair = try #require(binding.receipt)
            #expect(binding.locked && binding.error == nil && pair.image.currentVersion == 1 && pair.video.currentVersion == 1)
            await preview.load(.livePhoto(pair))
            #expect(preview.error == nil && preview.urls.count == 2)
            if preview.urls.count == 2 {
                try check(preview.urls[0],hash:f.image_sha256); try check(preview.urls[1],hash:f.video_sha256)
                try decode(preview.urls)
            }
            await preview.close()
            let list = LivePhotoState(session:session)
            await list.load(); #expect(list.page?.items.map(\.id) == [pair.id])
            await list.unbind(pair)
            #expect(list.error == nil && list.page?.items.isEmpty == true)
            // Explicit new operation after the fresh empty list, not a replay.
            let rebound = try await session.bindLivePhoto(imageID:f.image_id,videoID:f.video_id,expectedIdentity:identity)
            #expect(rebound.id != pair.id)
            list.discard(); binding.discard()
        } else if f.role == "pair-reader" {
            let page = try await session.livePhotos(page:1)
            let pair = try #require(page.items.first)
            #expect(page.items.count == 1 && pair.id == f.pair_id)
            await preview.load(.livePhoto(pair))
            #expect(preview.error == nil && preview.urls.count == 2)
            if preview.urls.count == 2 {
                try check(preview.urls[0],hash:f.image_sha256); try check(preview.urls[1],hash:f.video_sha256)
                try decode(preview.urls)
            }
            await preview.close()
            await #expect(throws:ServerError.forbidden) { try await session.unbindLivePhoto(id:pair.id,expectedIdentity:identity) }
            #expect(session.account != nil)
        } else {
            #expect(try await session.livePhotos(page:1).items.isEmpty)
            await #expect(throws:ServerError.forbidden) { try await session.livePhoto(id:f.pair_id) }
            await #expect(throws:ServerError.forbidden) { try await session.bindLivePhoto(imageID:f.image_id,videoID:f.video_id,expectedIdentity:identity) }
            if f.role == "revoked" {
                await preview.load(.livePhoto(try #require(f.pair)))
                #expect(preview.urls.isEmpty && preview.error == ServerError.forbidden.localizedDescription)
                await preview.close()
            }
        }
        #expect(transfers.records.values.allSatisfy { $0.previewID == nil })
        let cache = root.appending(path:"journal/preview-cache-v1")
        if FileManager.default.fileExists(atPath:cache.path) {
            #expect(try FileManager.default.contentsOfDirectory(atPath:cache.path) == [".format-v1"])
        }
        gallery.discard(); await transfers.close()
    }
}
