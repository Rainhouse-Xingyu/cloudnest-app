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
    private var telemetry: [UUID: TransferTelemetry] = [:]
    private var controls: Set<UUID> = []
    private var controlWaiters: [CheckedContinuation<Void, Never>] = []
    private var closeWaiters: [CheckedContinuation<Void, Never>] = []
    private var admissions = 0
    private var admissionWaiters: [CheckedContinuation<Void, Never>] = []
    private var schedulerWaiters: [CheckedContinuation<Void, Never>] = []
    private var scheduling = false
    private var restoration: Task<Void, Never>?

    var jobs: [TransferRecord] {
        records.values.filter { $0.identity == session.transferIdentity && $0.previewID == nil }.sorted { $0.createdAt > $1.createdAt }
    }

    func activity(_ id: UUID) -> TransferTelemetry.Snapshot? {
        guard let record = records[id], record.phase == .running,
              record.identity == session.transferIdentity else { return nil }
        return telemetry[id]?.snapshot()
    }

    init(session: SessionState, directory: URL = TransferStore.defaultDirectory,
         previewLimits: PreviewCache.Limits = .init()) {
        self.session = session
        self.directory = directory
        previews = PreviewCache(directory: directory.appending(path: "preview-cache-v1", directoryHint: .isDirectory), limits: previewLimits)
    }

    func restore() async {
        guard !closing, !restored else { return }
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
    func enqueueUpload(_ url: URL, parentID: Int64, expectedIdentity: TransferIdentity? = nil) async throws -> UUID {
        try beginAdmission()
        defer { finishAdmission() }
        let context = try session.transferContext()
        try requireIdentity(context.identity, expected: expectedIdentity)
        try await ensureStore()
        try requireIdentity(context.identity, expected: expectedIdentity)
        error = nil
        let name = url.lastPathComponent.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !name.isEmpty, parentID >= 0 else { throw ServerError.invalidOperation }
        let bookmark = try await disk.bookmark(url)
        try requireIdentity(context.identity, expected: expectedIdentity)
        let job = TransferRecord(id: UUID(), createdAt: Date(), identity: context.identity, direction: .upload,
                                 name: name, bookmark: bookmark, parentID: parentID, fileID: nil,
                                 isArchive: false, totalBytes: 0)
        try await persist(job)
        await schedule()
        return job.id
    }

    @discardableResult
    func enqueueDownload(_ file: FileItem, destination: URL, expectedIdentity: TransferIdentity? = nil) async throws -> UUID {
        try beginAdmission()
        defer { finishAdmission() }
        let context = try session.transferContext()
        try requireIdentity(context.identity, expected: expectedIdentity)
        try await ensureStore()
        try requireIdentity(context.identity, expected: expectedIdentity)
        error = nil
        let name = destination.lastPathComponent
        guard !name.isEmpty, name != ".", name != "..", file.id > 0 else { throw ServerError.invalidOperation }
        let bookmark = try await disk.bookmark(destination.deletingLastPathComponent())
        try requireIdentity(context.identity, expected: expectedIdentity)
        let job = TransferRecord(id: UUID(), createdAt: Date(), identity: context.identity, direction: .download,
                                 name: name, bookmark: bookmark, parentID: 0, fileID: file.id,
                                 isArchive: file.isDirectory, totalBytes: file.isDirectory ? 0 : file.size)
        try await persist(job)
        await schedule()
        return job.id
    }

    @discardableResult
    func enqueueVersionUpload(_ url: URL, file: FileItem, baseVersion: Int, comment: String, expectedIdentity: TransferIdentity? = nil) async throws -> UUID {
        try beginAdmission()
        defer { finishAdmission() }
        let context = try session.transferContext()
        try requireIdentity(context.identity, expected: expectedIdentity)
        try await ensureStore()
        try requireIdentity(context.identity, expected: expectedIdentity)
        guard !file.isDirectory, file.id > 0, baseVersion > 0, baseVersion < 2147483647,
              comment.unicodeScalars.count <= 255, !comment.contains("\0") else { throw ServerError.invalidOperation }
        let name = url.lastPathComponent.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !name.isEmpty else { throw ServerError.invalidOperation }
        let bookmark = try await disk.bookmark(url)
        try requireIdentity(context.identity, expected: expectedIdentity)
        var job = TransferRecord(id: UUID(), createdAt: Date(), identity: context.identity, direction: .versionUpload,
            name: name, bookmark: bookmark, parentID: 0, fileID: file.id, isArchive: false, totalBytes: 0)
        job.baseVersion = baseVersion
        job.versionComment = comment
        job.versionTargetName = file.filename
        error = nil
        try await persist(job)
        await schedule()
        return job.id
    }

    @discardableResult
    func enqueueVersionDownload(_ file: FileItem, version: FileVersionItem, destination: URL, expectedIdentity: TransferIdentity? = nil) async throws -> UUID {
        try beginAdmission()
        defer { finishAdmission() }
        let context = try session.transferContext()
        try requireIdentity(context.identity, expected: expectedIdentity)
        try await ensureStore()
        try requireIdentity(context.identity, expected: expectedIdentity)
        error = nil
        guard !file.isDirectory, file.id > 0, version.id > 0, version.version > 0,
              version.size.map({ $0 >= 0 }) ?? true else { throw ServerError.invalidOperation }
        let name = destination.lastPathComponent
        guard !name.isEmpty, name != ".", name != ".." else { throw ServerError.invalidOperation }
        let bookmark = try await disk.bookmark(destination.deletingLastPathComponent())
        try requireIdentity(context.identity, expected: expectedIdentity)
        var job = TransferRecord(id: UUID(), createdAt: Date(), identity: context.identity, direction: .versionDownload,
                                 name: name, bookmark: bookmark, parentID: 0, fileID: file.id,
                                 isArchive: false, totalBytes: version.size ?? 0)
        job.fileVersion = version.version
        job.versionSize = version.size
        try await persist(job)
        await schedule()
        return job.id
    }

    func enqueuePreview(_ file: FileItem, expectedIdentity: TransferIdentity? = nil) async throws -> UUID {
        try beginAdmission()
        defer { finishAdmission() }
        let context = try session.transferContext()
        try requireIdentity(context.identity, expected: expectedIdentity)
        try await ensureStore()
        try requireIdentity(context.identity, expected: expectedIdentity)
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
            try requireIdentity(context.identity, expected: expectedIdentity)
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
        telemetry[id]?.stop()
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
        guard !closing, var job = records[id], job.identity == session.transferIdentity,
              job.previewID == nil,
              active[id] == nil, !controls.contains(id), [.paused, .failed].contains(job.phase) else { return }
        admissions += 1
        defer { finishAdmission() }
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
        guard !closing else { return }
        admissions += 1
        defer { finishAdmission() }
        for job in Array(records.values) where job.previewID != nil && job.identity != session.transferIdentity {
            await dismissPreview(job.id)
        }
        for (id, task) in active where records[id]?.identity != session.transferIdentity {
            telemetry[id]?.stop()
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
        if closing {
            await withCheckedContinuation { closeWaiters.append($0) }
            return
        }
        closing = true
        defer {
            closing = false
            let waiters = closeWaiters
            closeWaiters.removeAll()
            for waiter in waiters { waiter.resume() }
        }
        if let restoration { await restoration.value }
        if admissions > 0 {
            await withCheckedContinuation { admissionWaiters.append($0) }
        }
        if scheduling {
            await withCheckedContinuation { schedulerWaiters.append($0) }
        }
        for job in Array(records.values) where job.previewID != nil { await dismissPreview(job.id) }
        for dismissal in Array(previewDismissals.values) { await dismissal.value }
        let ids = Array(active.keys)
        for id in ids { active[id]?.cancel() }
        for id in ids {
            if records[id]?.identity == session.transferIdentity { await control(id, cancel: false, duringClose: true) }
            await active[id]?.value
        }
        if !controls.isEmpty {
            await withCheckedContinuation { controlWaiters.append($0) }
        }
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

    private func beginAdmission() throws {
        guard !closing else { throw TransferError.localStorage }
        admissions += 1
    }

    private func finishAdmission() {
        admissions -= 1
        if admissions == 0 {
            let waiters = admissionWaiters
            admissionWaiters.removeAll()
            for waiter in waiters { waiter.resume() }
        }
    }

    private func requireIdentity(_ identity: TransferIdentity, expected: TransferIdentity?) throws {
        guard !closing else { throw TransferError.localStorage }
        try session.requireTransferIdentity(identity)
        guard expected == nil || expected == identity else { throw ServerError.sessionChanged }
    }

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
        defer {
            scheduling = false
            let waiters = schedulerWaiters
            schedulerWaiters.removeAll()
            for waiter in waiters { waiter.resume() }
        }
        let pending = records.values.filter { $0.phase == .queued && $0.identity == session.transferIdentity }
            .sorted { $0.createdAt < $1.createdAt }
        for var job in pending where active.count < 2 {
            guard active[job.id] == nil else { continue }
            do {
                let context = try session.transferContext()
                guard context.identity == job.identity else { continue }
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
                telemetry[job.id] = TransferTelemetry()
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
            guard let job = records[id], job.identity == context.identity else { throw TransferError.invalidCheckpoint }
            switch job.direction {
            case .upload, .versionUpload: try await runUpload(job, context: context, generation: generation)
            case .download, .versionDownload: try await runDownload(job, context: context, generation: generation)
            }
        } catch {
            if generations[id] == generation, var job = records[id], job.phase == .running {
                job.phase = Task.isCancelled ? .paused : .failed
                job.error = Task.isCancelled ? nil : failureMessage(error, job: job)
                records[id] = job
                do { try await persist(job) } catch { self.error = message(error) }
            }
            session.observeTransferFailure(error, context: context)
        }
        if generations[id] == generation { telemetry[id]?.stop() }
        await disk.closeDownload(id)
        if generations[id] == generation {
            active[id] = nil
            generations[id] = nil
            telemetry[id] = nil
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
        telemetry[job.id]?.transition(to: .checking)
        var status: UploadStatus
        if let id = job.uploadID {
            status = try await context.api.uploadProgress(context, id: id)
        } else {
            status = try await initializeUpload(job, context: context, source: source)
            job.uploadID = status.uploadID
            job.epoch = status.epoch
            try validateUpload(status, id: status.uploadID, source: source, job: job)
            if var current = records[job.id], current.phase == .pausing || current.phase == .cancelling {
                current.uploadID = status.uploadID
                current.epoch = status.epoch
                try await persist(current)
                throw CancellationError()
            }
            try await checkpoint(job, generation: generation)
        }
        guard let uploadID = job.uploadID else { throw TransferError.invalidCheckpoint }
        try validateUpload(status, id: uploadID, source: source, job: job)
        if status.status == 2 {
            status = try await context.api.controlUpload(context, id: uploadID, epoch: status.epoch, paused: false)
            try validateUploadBinding(status, job: job)
            status = try await context.api.uploadProgress(context, id: uploadID)
            try validateUpload(status, id: uploadID, source: source, job: job)
        }
        guard status.status == 0 || status.status == 1 else { throw ServerError.conflict }
        job.epoch = status.epoch
        let uploaded = Set(status.uploaded ?? [])
        job.confirmedBytes = status.status == 1 ? source.size : uploaded.reduce(Int64(0)) {
            $0 + min(Int64(source.chunkSize), source.size - Int64($1 * source.chunkSize))
        }
        try await checkpoint(job, generation: generation)
        telemetry[job.id]?.transition(to: .transferring)
        if status.status == 0 {
            let url = try await disk.resolve(job.bookmark)
            for index in source.chunks.indices where !uploaded.contains(index) {
                try Task.checkCancellation()
                let data = try await disk.uploadChunk(url, source: source, index: index)
                try await context.api.uploadChunk(context, id: uploadID, epoch: job.epoch, index: index, data: data,
                                                   telemetry: telemetry[job.id])
                job.confirmedBytes += Int64(data.count)
                try await checkpoint(job, generation: generation)
            }
        }
        telemetry[job.id]?.transition(to: .confirming)
        let result = try await context.api.completeUpload(context, id: uploadID, epoch: job.epoch, parentID: job.parentID)
        try validateUploadReceipt(result, job: job)
        job.resultFileID = result.id
        if job.direction == .versionUpload { job.publishedVersion = result.currentVersion }
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
        telemetry[job.id]?.transition(to: .transferring)
        var done = false
        while !done {
            try Task.checkCancellation()
            let probe = job.etag != nil && job.confirmedBytes == job.totalBytes
            let start = probe ? 0 : job.confirmedBytes
            // A stream stays bounded by DownloadStream acknowledgements and
            // four-MiB disk checkpoints, not by issuing one HTTP request per
            // block (which makes the server re-hash the full object each time).
            let end = probe ? 0 : Int64.max
            let full = job.etag == nil && job.totalBytes == 0 && !job.isArchive
            var (bytes, response) = try await context.api.downloadResponse(context.origin, token: context.token,
                fileID: fileID, archive: job.isArchive, version: job.fileVersion,
                start: full ? nil : start, end: probe ? 0 : nil, etag: job.etag)
            if response.statusCode == 416 {
                bytes.cancel()
                (bytes, response) = try await context.api.downloadResponse(context.origin, token: context.token,
                    fileID: fileID, archive: job.isArchive, version: job.fileVersion, start: nil, end: nil, etag: nil)
            }
            defer { bytes.cancel() }
            let plan = try DownloadPlan(response: response, record: job, requestedStart: start, requestedEnd: end)
            if job.previewID != nil { try await previews.validate(job.id, total: plan.total) }
            if plan.replaces {
                if (telemetry[job.id]?.snapshot().bodyBytes ?? 0) > 0 { telemetry[job.id]?.restartRepresentation() }
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
                telemetry[job.id]?.record(bytes: Int64(chunk.data.count))
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
        telemetry[job.id]?.transition(to: .verifying)
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

    private func control(_ id: UUID, cancel: Bool, duringClose: Bool = false) async {
        guard (!closing || duringClose), var job = records[id], job.identity == session.transferIdentity,
              job.previewID == nil,
              !controls.contains(id), job.phase != .completed, job.phase != .cancelled,
              let context = try? session.transferContext(), context.identity == job.identity else { return }
        controls.insert(id)
        telemetry[id]?.stop()
        defer {
            controls.remove(id)
            if controls.isEmpty {
                let waiters = controlWaiters
                controlWaiters.removeAll()
                for waiter in waiters { waiter.resume() }
            }
        }
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
            if job.direction.isUpload, cancel, job.uploadID == nil, job.source != nil {
                do {
                    let result = try await context.api.cancelUploadInitialization(context, requestID: job.id)
                    job.uploadID = result.uploadID
                    if let file = result.item {
                        try validateUploadReceipt(file, job: job)
                        job.resultFileID = file.id
                        if job.direction == .versionUpload { job.publishedVersion = file.currentVersion }
                        job.confirmedBytes = job.totalBytes
                        job.phase = .completed
                    } else { job.phase = .cancelled }
                    try await persist(job)
                    return
                } catch ServerError.notFound where job.direction == .upload {
                    // Older servers can still resolve the ordinary contract
                    // with the original idempotent initialization below.
                }
            }
            if job.direction.isUpload, job.uploadID == nil, let source = job.source {
                // Init may have committed before its response was lost. Resolve
                // the durable request key before acknowledging pause or cancel.
                let status = try await initializeUpload(job, context: context, source: source)
                try validateUpload(status, id: status.uploadID, source: source, job: job)
                job.uploadID = status.uploadID
                job.epoch = status.epoch
                try await persist(job)
            }
            if job.direction.isUpload, let uploadID = job.uploadID {
                let status = try await context.api.uploadProgress(context, id: uploadID)
                guard status.uploadID == uploadID, status.epoch >= 0 else { throw ServerError.invalidResponse }
                try validateUploadBinding(status, job: job)
                job.epoch = status.epoch
                if status.status == 1 {
                    let file = try await context.api.completeUpload(context, id: uploadID, epoch: status.epoch, parentID: job.parentID)
                    try validateUploadReceipt(file, job: job)
                    job.resultFileID = file.id
                    if job.direction == .versionUpload { job.publishedVersion = file.currentVersion }
                    job.confirmedBytes = job.totalBytes
                    job.phase = .completed
                    try await persist(job)
                    return
                }
                if cancel {
                    try await context.api.cancelUpload(context, id: uploadID)
                } else if status.status == 0 {
                    let paused = try await context.api.controlUpload(context, id: uploadID, epoch: status.epoch, paused: true)
                    try validateUploadBinding(paused, job: job)
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
            job.error = failureMessage(error, job: job)
            records[id] = job
            try? await persist(job)
            session.observeTransferFailure(error, context: context)
        }
        await schedule()
    }

    private func validateUpload(_ status: UploadStatus, id: String, source: UploadSource, job: TransferRecord) throws {
        try validateUploadBinding(status, job: job)
        let indices = status.uploaded ?? []
        guard status.uploadID == id, !id.isEmpty, id.utf8.count <= 128,
              id.utf8.allSatisfy({ (48...57).contains($0) || (65...90).contains($0) || (97...122).contains($0) || [45, 46, 95].contains($0) }),
              status.epoch >= 0, Set(indices).count == indices.count,
              indices.allSatisfy({ source.chunks.indices.contains($0) }) else { throw ServerError.invalidResponse }
    }

    private func initializeUpload(_ job: TransferRecord, context: TransferContext, source: UploadSource) async throws -> UploadStatus {
        if job.direction == .versionUpload {
            guard let fileID = job.fileID, let base = job.baseVersion, let comment = job.versionComment else { throw TransferError.invalidCheckpoint }
            return try await context.api.initializeVersionUpload(context, requestID: job.id, name: job.name,
                fileID: fileID, baseVersion: base, comment: comment, source: source)
        }
        return try await context.api.initializeUpload(context, requestID: job.id, name: job.name, parentID: job.parentID, source: source)
    }

    private func validateUploadBinding(_ status: UploadStatus, job: TransferRecord) throws {
        if job.direction == .versionUpload {
            guard status.kind == "file_version", status.fileID == job.fileID, status.baseVersion == job.baseVersion else { throw ServerError.invalidResponse }
        } else {
            guard status.kind == nil, status.fileID == nil, status.baseVersion == nil else { throw ServerError.invalidResponse }
        }
    }

    private func validateUploadReceipt(_ file: FileItem, job: TransferRecord) throws {
        guard file.id > 0, !file.isDirectory, file.size == job.totalBytes else { throw ServerError.invalidResponse }
        if job.direction == .versionUpload {
            guard file.id == job.fileID, let base = job.baseVersion, file.currentVersion == base + 1 else { throw ServerError.invalidResponse }
        } else {
            guard file.parentID == job.parentID else { throw ServerError.invalidResponse }
        }
    }

    private func failureMessage(_ error: any Error, job: TransferRecord) -> String {
        if job.direction == .versionUpload, error as? ServerError == .conflict {
            return "版本基线或上传状态已变化。请取消此任务，刷新版本历史后重新选择文件上传；不会自动覆盖新版本。"
        }
        return message(error)
    }

    private func message(_ error: any Error) -> String {
        if let error = error as? ServerError { return error.localizedDescription }
        if let error = error as? TransferError { return error.localizedDescription }
        return "传输未完成，请检查网络、磁盘空间和文件访问权限后重试。"
    }
}
