import Foundation
import Observation
import CryptoKit

@MainActor
@Observable
final class TransferManager {
    private(set) var records: [UUID: TransferRecord] = [:]
    private(set) var error: String?
    private(set) var restored = false
    private let session: SessionState
    private let directory: URL
    private let disk = TransferDisk()
    private let previews: PreviewCache
    private var previewDismissals: [UUID: Task<Void, Never>] = [:]
    private var abandonedPreviews: Set<UUID> = []
    private var closing = false
    private var store: TransferStore?
    private var active: [UUID: Task<Void, Never>] = [:]
    private var generations: [UUID: UUID] = [:]
    private var controls: Set<UUID> = []
    private var scheduling = false
    private var restoration: Task<Void, Never>?

    var jobs: [TransferRecord] {
        records.values.filter { $0.identity == session.transferIdentity && $0.previewID == nil }.sorted { $0.createdAt > $1.createdAt }
    }

    init(session: SessionState, directory: URL = TransferStore.defaultDirectory,
         previewLimits: PreviewCache.Limits = .init()) {
        self.session = session
        self.directory = directory
        previews = PreviewCache(directory: directory.appending(path: "preview-cache-v1", directoryHint: .isDirectory), limits: previewLimits)
    }

    func restore() async {
        guard !restored else { return }
        if let restoration { await restoration.value; return }
        let task = Task { await self.loadStore() }
        restoration = task
        await task.value
        restoration = nil
    }

    private func loadStore() async {
        do {
            let store = try TransferStore(directory: directory)
            self.store = store
            let saved = try await store.load()
            for var record in saved {
                if record.phase.isActive || record.phase == .queued {
                    record.phase = record.cancelRequested ? .failed : .paused
                    record.error = "上次传输已中断，可继续或取消。"
                    try await store.save(record)
                }
                records[record.id] = record
            }
            restored = true
            error = nil
            do { try await previews.recover() }
            catch { self.error = message(error) }
        } catch {
            store = nil
            self.error = message(error)
        }
    }

    @discardableResult
    func enqueueUpload(_ url: URL, parentID: Int64) async throws -> UUID {
        try await ensureStore()
        let context = try session.transferContext()
        error = nil
        let name = url.lastPathComponent.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !name.isEmpty, parentID >= 0 else { throw ServerError.invalidOperation }
        let bookmark = try await disk.bookmark(url)
        let job = TransferRecord(id: UUID(), createdAt: Date(), identity: context.identity, direction: .upload,
                                 name: name, bookmark: bookmark, parentID: parentID, fileID: nil,
                                 isArchive: false, totalBytes: 0)
        try await persist(job)
        await schedule()
        return job.id
    }

    @discardableResult
    func enqueueDownload(_ file: FileItem, destination: URL) async throws -> UUID {
        try await ensureStore()
        let context = try session.transferContext()
        error = nil
        let name = destination.lastPathComponent
        guard !name.isEmpty, name != ".", name != "..", file.id > 0 else { throw ServerError.invalidOperation }
        let bookmark = try await disk.bookmark(destination.deletingLastPathComponent())
        let job = TransferRecord(id: UUID(), createdAt: Date(), identity: context.identity, direction: .download,
                                 name: name, bookmark: bookmark, parentID: 0, fileID: file.id,
                                 isArchive: file.isDirectory, totalBytes: file.isDirectory ? 0 : file.size)
        try await persist(job)
        await schedule()
        return job.id
    }

    @discardableResult
    func enqueueVersionDownload(_ file: FileItem, version: FileVersionItem, destination: URL) async throws -> UUID {
        try await ensureStore()
        let context = try session.transferContext()
        error = nil
        guard !file.isDirectory, file.id > 0, version.id > 0, version.version > 0,
              version.size.map({ $0 >= 0 }) ?? true else { throw ServerError.invalidOperation }
        let name = destination.lastPathComponent
        guard !name.isEmpty, name != ".", name != ".." else { throw ServerError.invalidOperation }
        let bookmark = try await disk.bookmark(destination.deletingLastPathComponent())
        var job = TransferRecord(id: UUID(), createdAt: Date(), identity: context.identity, direction: .versionDownload,
                                 name: name, bookmark: bookmark, parentID: 0, fileID: file.id,
                                 isArchive: false, totalBytes: version.size ?? 0)
        job.fileVersion = version.version
        job.versionSize = version.size
        try await persist(job)
        await schedule()
        return job.id
    }

    func enqueuePreview(_ file: FileItem) async throws -> UUID {
        try await ensureStore()
        let context = try session.transferContext()
        guard file.id > 0, !file.isDirectory, !file.filename.isEmpty, file.filename != ".", file.filename != "..",
              URL(fileURLWithPath: file.filename).lastPathComponent == file.filename else { throw ServerError.invalidResponse }
        for id in Array(abandonedPreviews) { await dismissPreview(id) }
        error = nil
        let id = UUID()
        let folder = try await previews.reserve(id, bytes: file.size)
        do {
            let bookmark = try await disk.bookmark(folder)
            var job = TransferRecord(id: id, createdAt: Date(), identity: context.identity, direction: .download,
                                     name: file.filename, bookmark: bookmark, parentID: 0, fileID: file.id,
                                     isArchive: false, totalBytes: file.size)
            guard context.identity == session.transferIdentity else { throw ServerError.unauthorized }
            job.previewID = id
            try await persist(job)
            await schedule()
            return id
        } catch {
            records[id] = nil
            try? await previews.remove(id)
            throw error
        }
    }

    func dismissPreview(_ id: UUID) async {
        if let dismissal = previewDismissals[id] { await dismissal.value; return }
        guard records[id]?.previewID == id else { return }
        let dismissal = Task {
            await self.removePreview(id)
            self.previewDismissals[id] = nil
        }
        previewDismissals[id] = dismissal
        await dismissal.value
    }

    private func removePreview(_ id: UUID) async {
        abandonedPreviews.insert(id)
        records[id]?.phase = .cancelling
        active[id]?.cancel()
        await active[id]?.value
        await disk.closeDownload(id)
        do {
            try await previews.remove(id)
            records[id] = nil
            abandonedPreviews.remove(id)
        } catch {
            records[id]?.phase = .failed
            records[id]?.error = message(error)
            self.error = message(error)
        }
        await schedule()
    }

    func resume(_ id: UUID) async {
        guard var job = records[id], job.identity == session.transferIdentity,
              job.previewID == nil,
              active[id] == nil, !controls.contains(id), [.paused, .failed].contains(job.phase) else { return }
        if job.cancelRequested { await cancel(id); return }
        job.phase = .queued
        job.error = nil
        do {
            try await persist(job)
            await schedule()
        } catch { self.error = message(error) }
    }

    func pause(_ id: UUID) async { await control(id, cancel: false) }
    func cancel(_ id: UUID) async { await control(id, cancel: true) }

    func synchronizeIdentity() async {
        for job in Array(records.values) where job.previewID != nil && job.identity != session.transferIdentity {
            await dismissPreview(job.id)
        }
        for (id, task) in active where records[id]?.identity != session.transferIdentity {
            task.cancel()
            if var job = records[id] {
                job.phase = .paused
                records[id] = job
                try? await persist(job)
            }
        }
        await schedule()
    }

    func waitUntilIdle(_ id: UUID) async {
        await active[id]?.value
    }

    func close() async {
        closing = true
        defer { closing = false }
        for job in Array(records.values) where job.previewID != nil { await dismissPreview(job.id) }
        for dismissal in Array(previewDismissals.values) { await dismissal.value }
        for id in Array(active.keys) { await pause(id) }
        store = nil
        restored = false
    }

    func resultURL(_ id: UUID) async throws -> URL {
        guard let job = records[id], job.identity == session.transferIdentity,
              job.direction.isDownload, job.phase == .completed, let name = job.resultName else {
            throw TransferError.invalidCheckpoint
        }
        return try await disk.resolve(job.bookmark).appending(path: name)
    }

    func report(_ error: any Error) { self.error = message(error) }

    private func ensureStore() async throws {
        if !restored { await restore() }
        guard store != nil, restored else { throw TransferError.localStorage }
    }

    private func persist(_ value: TransferRecord) async throws {
        guard let store else { throw TransferError.localStorage }
        var job = value
        job.revision = (records[job.id]?.revision ?? -1) + 1
        records[job.id] = job
        do { if job.previewID == nil { try await store.save(job) } }
        catch {
            if records[job.id]?.revision == job.revision {
                records[job.id]?.phase = .failed
                records[job.id]?.error = message(error)
            }
            throw error
        }
    }

    private func checkpoint(_ value: TransferRecord, generation: UUID) async throws {
        guard generations[value.id] == generation, records[value.id]?.phase == .running else { throw CancellationError() }
        guard let store else { throw TransferError.localStorage }
        var job = value
        job.revision = (records[job.id]?.revision ?? 0) + 1
        records[job.id]?.revision = job.revision
        if job.previewID == nil { try await store.save(job) }
        guard generations[job.id] == generation, records[job.id]?.phase == .running,
              records[job.id]?.revision == job.revision else { throw CancellationError() }
        records[job.id] = job
    }

    private func schedule() async {
        guard restored, !scheduling, !closing else { return }
        scheduling = true
        defer { scheduling = false }
        let pending = records.values.filter { $0.phase == .queued && $0.identity == session.transferIdentity }
            .sorted { $0.createdAt < $1.createdAt }
        for var job in pending where active.count < 2 {
            guard active[job.id] == nil else { continue }
            do {
                let context = try session.transferContext()
                job.phase = .running
                try await persist(job)
                guard records[job.id]?.phase == .running, !controls.contains(job.id) else { continue }
                guard job.identity == session.transferIdentity else {
                    job.phase = .paused
                    try await persist(job)
                    continue
                }
                let generation = UUID()
                generations[job.id] = generation
                let id = job.id
                active[id] = Task { await self.run(id, context: context, generation: generation) }
            } catch {
                self.error = message(error)
                return
            }
        }
    }

    private func run(_ id: UUID, context: TransferContext, generation: UUID) async {
        do {
            guard let job = records[id] else { throw TransferError.invalidCheckpoint }
            switch job.direction {
            case .upload: try await runUpload(job, context: context, generation: generation)
            case .download, .versionDownload: try await runDownload(job, context: context, generation: generation)
            }
        } catch {
            if generations[id] == generation, var job = records[id], job.phase == .running {
                job.phase = Task.isCancelled ? .paused : .failed
                job.error = Task.isCancelled ? nil : message(error)
                records[id] = job
                do { try await persist(job) } catch { self.error = message(error) }
            }
            session.observeTransferFailure(error, identity: context.identity)
        }
        await disk.closeDownload(id)
        if generations[id] == generation {
            active[id] = nil
            generations[id] = nil
        }
        await schedule()
    }

    private func runUpload(_ initial: TransferRecord, context: TransferContext, generation: UUID) async throws {
        var job = initial
        if job.source == nil {
            let url = try await disk.resolve(job.bookmark)
            let source = try await disk.prepareUpload(url)
            job.source = source
            job.totalBytes = source.size
            try await checkpoint(job, generation: generation)
        }
        guard let source = job.source else { throw TransferError.invalidCheckpoint }
        var status: UploadStatus
        if let id = job.uploadID {
            status = try await context.api.uploadProgress(context, id: id)
        } else {
            status = try await context.api.initializeUpload(context, requestID: job.id, name: job.name, parentID: job.parentID, source: source)
            job.uploadID = status.uploadID
            job.epoch = status.epoch
            try validateUpload(status, id: status.uploadID, source: source)
            if var current = records[job.id], current.phase == .pausing || current.phase == .cancelling {
                current.uploadID = status.uploadID
                current.epoch = status.epoch
                try await persist(current)
                throw CancellationError()
            }
            try await checkpoint(job, generation: generation)
        }
        guard let uploadID = job.uploadID else { throw TransferError.invalidCheckpoint }
        try validateUpload(status, id: uploadID, source: source)
        if status.status == 2 {
            status = try await context.api.controlUpload(context, id: uploadID, epoch: status.epoch, paused: false)
            status = try await context.api.uploadProgress(context, id: uploadID)
            try validateUpload(status, id: uploadID, source: source)
        }
        guard status.status == 0 || status.status == 1 else { throw ServerError.conflict }
        job.epoch = status.epoch
        let uploaded = Set(status.uploaded ?? [])
        job.confirmedBytes = status.status == 1 ? source.size : uploaded.reduce(Int64(0)) {
            $0 + min(Int64(source.chunkSize), source.size - Int64($1 * source.chunkSize))
        }
        try await checkpoint(job, generation: generation)
        if status.status == 0 {
            let url = try await disk.resolve(job.bookmark)
            for index in source.chunks.indices where !uploaded.contains(index) {
                try Task.checkCancellation()
                let data = try await disk.uploadChunk(url, source: source, index: index)
                try await context.api.uploadChunk(context, id: uploadID, epoch: job.epoch, index: index, data: data)
                job.confirmedBytes += Int64(data.count)
                try await checkpoint(job, generation: generation)
            }
        }
        let result = try await context.api.completeUpload(context, id: uploadID, epoch: job.epoch, parentID: job.parentID)
        guard result.id > 0, result.size == source.size else { throw ServerError.invalidResponse }
        job.resultFileID = result.id
        job.confirmedBytes = source.size
        job.phase = .completed
        try await checkpoint(job, generation: generation)
    }

    private func runDownload(_ initial: TransferRecord, context: TransferContext, generation: UUID) async throws {
        var job = initial
        guard let fileID = job.fileID else { throw TransferError.invalidCheckpoint }
        let directory = try await disk.resolve(job.bookmark)
        let access = directory.startAccessingSecurityScopedResource()
        defer { if access { directory.stopAccessingSecurityScopedResource() } }
        try await disk.openDownload(job, directory: directory)
        var done = false
        while !done {
            try Task.checkCancellation()
            let probe = job.etag != nil && job.confirmedBytes == job.totalBytes
            let start = probe ? 0 : job.confirmedBytes
            guard start <= Int64.max - Int64(TransferDisk.downloadChunkSize) else { throw TransferError.invalidDownload }
            let end = probe ? 0 : start + Int64(TransferDisk.downloadChunkSize) - 1
            let full = job.etag == nil && job.totalBytes == 0 && !job.isArchive
            var (bytes, response) = try await context.api.downloadResponse(context.origin, token: context.token,
                fileID: fileID, archive: job.isArchive, version: job.fileVersion, start: full ? nil : start, end: full ? nil : end, etag: job.etag)
            if response.statusCode == 416 {
                bytes.cancel()
                (bytes, response) = try await context.api.downloadResponse(context.origin, token: context.token,
                    fileID: fileID, archive: job.isArchive, version: job.fileVersion, start: nil, end: nil, etag: nil)
            }
            defer { bytes.cancel() }
            let plan = try DownloadPlan(response: response, record: job, requestedStart: start, requestedEnd: end)
            if job.previewID != nil { try await previews.validate(job.id, total: plan.total) }
            if plan.replaces {
                try await disk.resetDownload(job.id)
                job.confirmedBytes = 0
                job.downloadedChunks = []
                job.resultName = nil
            }
            job.totalBytes = plan.total
            job.etag = plan.etag
            job.sha256 = plan.sha256
            try await checkpoint(job, generation: generation)
            var received: Int64 = 0
            var buffer = Data()
            let probeOnly = probe && !plan.replaces
            for try await chunk in bytes.chunks {
                defer { chunk.acknowledge() }
                try Task.checkCancellation()
                guard Int64(chunk.data.count) <= plan.length - received else { throw TransferError.invalidDownload }
                received += Int64(chunk.data.count)
                if probeOnly { continue }
                var offset = 0
                while offset < chunk.data.count {
                    let count = min(TransferDisk.downloadChunkSize - buffer.count, chunk.data.count - offset)
                    buffer.append(chunk.data.subdata(in: offset..<(offset + count)))
                    offset += count
                    if buffer.count == TransferDisk.downloadChunkSize {
                        try await storeDownloadChunk(&job, data: buffer, generation: generation)
                        buffer.removeAll(keepingCapacity: true)
                    }
                }
            }
            guard received == plan.length else { throw TransferError.invalidDownload }
            if !buffer.isEmpty { try await storeDownloadChunk(&job, data: buffer, generation: generation) }
            done = probeOnly || job.confirmedBytes == job.totalBytes
        }
        // Publication name is saved before the atomic link. Retrying a crash
        // reuses a link to the same staging inode and preserves existing files.
        var suffix = 0
        while true {
            job.resultName = job.resultName ?? TransferDisk.availableName(job.name, suffix: suffix)
            try await checkpoint(job, generation: generation)
            do {
                job.resultName = try await disk.publishDownload(job, directory: directory)
                break
            } catch TransferError.destinationExists {
                suffix += 1
                guard suffix <= 9999 else { throw TransferError.localStorage }
                job.resultName = nil
            }
        }
        job.phase = .completed
        try await checkpoint(job, generation: generation)
        do { try await disk.discardDownload(job.id, directory: directory) }
        catch { self.error = "文件已保存，临时文件清理失败，请检查目标目录权限。" }
    }

    private func storeDownloadChunk(_ job: inout TransferRecord, data: Data, generation: UUID) async throws {
        try Task.checkCancellation()
        guard Int64(data.count) <= job.totalBytes - job.confirmedBytes else { throw TransferError.invalidDownload }
        try await disk.appendDownload(job.id, data: data, offset: job.confirmedBytes)
        try await disk.synchronizeDownload(job.id)
        job.confirmedBytes += Int64(data.count)
        job.downloadedChunks.append(TransferDisk.hex(SHA256.hash(data: data)))
        try await checkpoint(job, generation: generation)
    }

    private func control(_ id: UUID, cancel: Bool) async {
        guard var job = records[id], job.identity == session.transferIdentity,
              job.previewID == nil,
              !controls.contains(id), job.phase != .completed, job.phase != .cancelled else { return }
        controls.insert(id)
        defer { controls.remove(id) }
        job.phase = cancel ? .cancelling : .pausing
        job.cancelRequested = cancel
        job.error = nil
        records[id] = job
        active[id]?.cancel()
        await active[id]?.value
        job = records[id] ?? job
        job.phase = cancel ? .cancelling : .pausing
        job.cancelRequested = cancel
        do {
            try await persist(job)
            let context = try session.transferContext()
            if job.direction == .upload, job.uploadID == nil, let source = job.source {
                // Init may have committed before its response was lost. Resolve
                // the durable request key before acknowledging pause or cancel.
                let status = try await context.api.initializeUpload(context, requestID: job.id,
                    name: job.name, parentID: job.parentID, source: source)
                try validateUpload(status, id: status.uploadID, source: source)
                job.uploadID = status.uploadID
                job.epoch = status.epoch
                try await persist(job)
            }
            if job.direction == .upload, let uploadID = job.uploadID {
                let status = try await context.api.uploadProgress(context, id: uploadID)
                guard status.uploadID == uploadID, status.epoch >= 0 else { throw ServerError.invalidResponse }
                job.epoch = status.epoch
                if status.status == 1 {
                    let file = try await context.api.completeUpload(context, id: uploadID, epoch: status.epoch, parentID: job.parentID)
                    guard file.id > 0, file.size == job.totalBytes else { throw ServerError.invalidResponse }
                    job.resultFileID = file.id
                    job.confirmedBytes = job.totalBytes
                    job.phase = .completed
                    try await persist(job)
                    return
                }
                if cancel {
                    try await context.api.cancelUpload(context, id: uploadID)
                } else if status.status == 0 {
                    let paused = try await context.api.controlUpload(context, id: uploadID, epoch: status.epoch, paused: true)
                    guard paused.status == 2, paused.uploadID == uploadID, paused.epoch >= status.epoch else {
                        throw ServerError.invalidResponse
                    }
                    job.epoch = paused.epoch
                } else if status.status != 2 { throw ServerError.conflict }
            }
            if job.direction.isDownload {
                let directory = try await disk.resolve(job.bookmark)
                let access = directory.startAccessingSecurityScopedResource()
                defer { if access { directory.stopAccessingSecurityScopedResource() } }
                if await disk.publicationExists(job, directory: directory) {
                    job.phase = .completed
                    try await persist(job)
                    try? await disk.discardDownload(id, directory: directory)
                    return
                }
                if cancel { try await disk.discardDownload(id, directory: directory) }
            }
            job.phase = cancel ? .cancelled : .paused
            try await persist(job)
        } catch {
            job.phase = .failed
            job.error = message(error)
            records[id] = job
            try? await persist(job)
            session.observeTransferFailure(error, identity: job.identity)
        }
        await schedule()
    }

    private func validateUpload(_ status: UploadStatus, id: String, source: UploadSource) throws {
        let indices = status.uploaded ?? []
        guard status.uploadID == id, !id.isEmpty, id.utf8.count <= 128,
              id.utf8.allSatisfy({ (48...57).contains($0) || (65...90).contains($0) || (97...122).contains($0) || [45, 46, 95].contains($0) }),
              status.epoch >= 0, Set(indices).count == indices.count,
              indices.allSatisfy({ source.chunks.indices.contains($0) }) else { throw ServerError.invalidResponse }
    }

    private func message(_ error: any Error) -> String {
        if let error = error as? ServerError { return error.localizedDescription }
        if let error = error as? TransferError { return error.localizedDescription }
        return "传输未完成，请检查网络、磁盘空间和文件访问权限后重试。"
    }
}
