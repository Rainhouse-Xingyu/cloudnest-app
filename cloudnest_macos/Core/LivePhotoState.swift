import Foundation
import Observation

@MainActor @Observable
final class LivePhotoState {
    private let session: SessionState
    private let identity: TransferIdentity?
    private var operation: Task<Void,Never>?
    private var generation = UUID()
    private(set) var page: LivePhotoPage?
    private(set) var busy = false
    private(set) var mutating = false
    private(set) var error: String?

    init(session: SessionState) { self.session = session; identity = session.transferIdentity }
    func load(page number: Int = 1) async {
        guard !mutating else { return }
        operation?.cancel()
        let id = UUID(); generation = id; busy = true; page = nil; error = nil
        let task = Task {
            defer { if generation == id { busy = false; operation = nil } }
            do {
                try session.requireTransferIdentity(identity)
                let result = try await session.livePhotos(page:number)
                try Task.checkCancellation(); try session.requireTransferIdentity(identity)
                guard generation == id else { return }
                page = result
            } catch is CancellationError { return }
            catch {
                if generation == id { self.error = (error as? ServerError)?.localizedDescription ?? "Live Photo 列表读取失败，请刷新。" }
            }
        }
        operation = task; await task.value
    }
    func unbind(_ pair: LivePhotoPair) async {
        guard !busy, page?.items.contains(pair) == true else { return }
        let id = UUID(); generation = id
        mutating = true; busy = true; error = nil
        do {
            try session.requireTransferIdentity(identity)
            try await session.unbindLivePhoto(id:pair.id,expectedIdentity:identity)
            try session.requireTransferIdentity(identity)
            guard generation == id else { mutating = false; busy = false; return }
            mutating = false; busy = false
            await load()
        } catch {
            guard generation == id else { mutating = false; busy = false; return }
            page = nil; busy = false; mutating = false
            self.error = (error as? ServerError)?.localizedDescription ?? "解绑结果未确认。请刷新列表核对，不要直接重复提交；源文件不会因解绑而删除。"
        }
    }
    func discard() {
        generation = UUID(); operation?.cancel(); operation = nil
        page = nil; error = nil
        if !mutating { busy = false }
    }
}

@MainActor @Observable
final class LivePhotoBindingState {
    private let session: SessionState
    private let identity: TransferIdentity?
    let image: FileItem
    private var operation: Task<Void,Never>?
    private var generation = UUID()
    private(set) var page: MotionCandidatePage?
    private(set) var query: PhotoQuery?
    private(set) var busy = false
    private(set) var submitting = false
    private(set) var locked = false
    private(set) var receipt: LivePhotoPair?
    private(set) var error: String?

    init(session: SessionState, image: FileItem) {
        self.session = session; self.image = image; identity = session.transferIdentity
    }
    func load(_ query: PhotoQuery, page number: Int = 1) async {
        guard !submitting, !locked else { return }
        operation?.cancel()
        let id = UUID(); generation = id; self.query = query; page = nil; busy = true; error = nil
        let task = Task {
            defer { if generation == id { busy = false; operation = nil } }
            do {
                try session.requireTransferIdentity(identity)
                let result = try await session.motionCandidates(query:query,page:number)
                try Task.checkCancellation(); try session.requireTransferIdentity(identity)
                guard generation == id else { return }
                page = result
            } catch is CancellationError { return }
            catch {
                if generation == id { self.error = (error as? ServerError)?.localizedDescription ?? "动态片段读取失败，请关闭后重试。" }
            }
        }
        operation = task; await task.value
    }
    func bind(_ video: FileItem) async {
        guard !busy, !locked, page?.items.contains(video) == true else { return }
        operation?.cancel(); let id = UUID(); generation = id
        busy = true; submitting = true; error = nil
        defer { busy = false; submitting = false }
        do {
            try session.requireTransferIdentity(identity)
            guard PhotoContract.valid(image), ["jpg","jpeg","heic","heif"].contains((image.filename as NSString).pathExtension.lowercased()) else { throw ServerError.invalidOperation }
            let pair = try await session.bindLivePhoto(imageID:image.id,videoID:video.id,expectedIdentity:identity)
            try session.requireTransferIdentity(identity)
            guard generation == id else { return }
            receipt = pair; locked = true
        } catch {
            guard generation == id else { return }
            locked = true; page = nil
            self.error = ((error as? ServerError)?.localizedDescription ?? "关联结果未确认。") + " 请关闭后刷新 Live Photo 列表核对，不要在此表单重复提交。"
        }
    }
    func discard() {
        generation = UUID(); operation?.cancel(); operation = nil
        page = nil; receipt = nil
        if !submitting { busy = false }
    }
}
