import Foundation
import Observation

@MainActor
@Observable
final class AISearchState {
    var question = ""
    private(set) var submittedQuestion = ""
    private(set) var response: AISearchResponse?
    private(set) var busy = false
    private(set) var error: String?
    private(set) var searched = false
    private var revision = UUID()
    private var request: Task<AISearchResponse, any Error>?
    private let session: SessionState
    private let identity: TransferIdentity?

    init(session: SessionState) {
        self.session = session
        identity = session.transferIdentity
    }

    var canSubmit: Bool {
        let value = question.trimmingCharacters(in: .whitespacesAndNewlines)
        return !value.isEmpty && value.utf8.count <= 4096
    }
    var page: Int { response?.pagination.page ?? 1 }
    var hasNext: Bool {
        guard let response else { return false }
        return page * response.pagination.limit < min(response.pagination.total, response.retrieval.windowSize)
    }

    func submit() async {
        guard canSubmit else { return }
        await load(question: question.trimmingCharacters(in: .whitespacesAndNewlines), page: 1)
    }

    func go(to page: Int) async {
        guard !busy, !submittedQuestion.isEmpty, (1...10).contains(page) else { return }
        await load(question: submittedQuestion, page: page)
    }

    func cancel() {
        revision = UUID()
        request?.cancel()
        request = nil
        busy = false
    }

    private func load(question: String, page: Int) async {
        cancel()
        let revision = self.revision
        busy = true
        response = nil
        error = nil
        searched = true
        submittedQuestion = question
        let task = Task {
            try session.requireTransferIdentity(identity)
            try Task.checkCancellation()
            return try await session.searchAI(question: question, page: page)
        }
        request = task
        defer {
            if self.revision == revision { busy = false; request = nil }
        }
        do {
            try session.requireTransferIdentity(identity)
            let result = try await withTaskCancellationHandler { try await task.value } onCancel: { task.cancel() }
            try Task.checkCancellation()
            guard self.revision == revision else { return }
            try session.requireTransferIdentity(identity)
            response = result
        } catch {
            guard self.revision == revision, !task.isCancelled, !(error is CancellationError) else { return }
            response = nil
            self.error = (error as? ServerError)?.localizedDescription ?? "检索未完成，请检查连接后重试。"
        }
    }
}

@MainActor
@Observable
final class AIIndexState {
    private(set) var items: [AIIndexItem] = []
    private(set) var after: Int64 = 0
    private(set) var nextAfter: Int64 = 0
    private(set) var hasMore = false
    private(set) var busy = false
    private(set) var loaded = false
    private(set) var error: String?
    private(set) var notice: String?
    private let session: SessionState
    private let identity: TransferIdentity?

    init(session: SessionState) {
        self.session = session
        identity = session.transferIdentity
    }

    func load(after: Int64 = 0) async {
        guard !busy else { return }
        busy = true
        error = nil
        notice = nil
        defer { busy = false }
        do {
            try session.requireTransferIdentity(identity)
            let response = try await session.indexAI(after: after)
            try Task.checkCancellation()
            try session.requireTransferIdentity(identity)
            items = response.items
            self.after = after
            nextAfter = response.nextAfterID
            hasMore = response.hasMore
            loaded = true
        } catch {
            items = []
            hasMore = false
            loaded = true
            if !(error is CancellationError) {
                self.error = (error as? ServerError)?.localizedDescription ?? "无法读取索引状态，请重试。"
            }
        }
    }

    func rebuild(_ item: AIIndexItem) async {
        guard !busy, items.contains(where: { $0.id == item.id && $0.generation == item.generation }) else { return }
        busy = true
        error = nil
        notice = nil
        do {
            try session.requireTransferIdentity(identity)
            try await session.rebuildAI(item)
            try session.requireTransferIdentity(identity)
            busy = false
            await load(after: after)
            if error == nil { notice = "已加入重建队列。处理需要时间，可刷新查看当前状态。" }
        } catch {
            busy = false
            if error as? ServerError == .conflict {
                await load(after: after)
                if self.error == nil { self.error = "索引已变化，已重新读取状态。请核对后再次选择重建。" }
            } else {
                items = []
                hasMore = false
                self.error = (error as? ServerError)?.localizedDescription ?? "未能提交重建，请刷新后重试。"
            }
        }
    }
}
