import Foundation
import Observation

private enum PhotoThumbnailResult: Sendable {
    case image(Int64,Data)
    case failure(Int64,String)
}

@MainActor @Observable
final class PhotoState {
    private let session: SessionState
    private let identity: TransferIdentity?
    private var operation: Task<Void,Never>?
    private var generation = UUID()
    private(set) var page: PhotoPage?
    private(set) var busy = false
    private(set) var thumbnails: [Int64:Data] = [:]
    private(set) var thumbnailErrors: [Int64:String] = [:]
    private(set) var error: String?

    init(session: SessionState) { self.session = session; identity = session.transferIdentity }

    func load(_ query: PhotoQuery, page number: Int = 1) async {
        operation?.cancel()
        let id = UUID(); generation = id
        busy = true; page = nil; thumbnails = [:]; thumbnailErrors = [:]; error = nil
        let task = Task {
            defer { if generation == id { busy = false; operation = nil } }
            do {
                try session.requireTransferIdentity(identity)
                let result = try await session.photos(query:query,page:number)
                try Task.checkCancellation(); try session.requireTransferIdentity(identity)
                guard generation == id else { return }
                page = result; busy = false
                // At most four requests and fifty 512-KiB encoded thumbnails.
                // Page replacement cancels requests and drops the whole cache.
                let fetch: @Sendable (FileItem) async -> PhotoThumbnailResult = { [session] file in
                    do { return .image(file.id,try await session.photoThumbnail(fileID:file.id)) }
                    catch { return .failure(file.id,(error as? ServerError)?.localizedDescription ?? "缩略图未能加载，可刷新或预览原图。") }
                }
                try await withThrowingTaskGroup(of:PhotoThumbnailResult.self) { group in
                    var next = 0
                    while next < min(4,result.items.count) {
                        let file = result.items[next]
                        group.addTask { await fetch(file) }; next += 1
                    }
                    for try await value in group {
                        try Task.checkCancellation(); try session.requireTransferIdentity(identity)
                        guard generation == id else { group.cancelAll(); return }
                        switch value {
                        case let .image(fileID,data): thumbnails[fileID] = data
                        case let .failure(fileID,message): thumbnailErrors[fileID] = message
                        }
                        if next < result.items.count {
                            let file = result.items[next]
                            group.addTask { await fetch(file) }; next += 1
                        }
                    }
                }
            } catch is CancellationError { return }
            catch {
                guard generation == id else { return }
                page = nil; thumbnails = [:]; thumbnailErrors = [:]
                self.error = (error as? ServerError)?.localizedDescription ?? "照片读取失败，请检查连接后刷新。"
            }
        }
        operation = task; await task.value
    }

    func next() async {
        guard !busy,let page,page.hasNext else { return }
        await load(page.query,page:page.page+1)
    }
    func previous() async {
        guard !busy,let page,page.page>1 else { return }
        await load(page.query,page:page.page-1)
    }
    func discard() {
        generation = UUID(); operation?.cancel(); operation = nil
        page = nil; thumbnails = [:]; thumbnailErrors = [:]; busy = false; error = nil
    }
}
