import Foundation
import Combine

@MainActor
final class AdminLogViewModel: ObservableObject {
    @Published var logs: [OperationLogItemDTO] = []
    @Published var isLoading: Bool = false
    @Published var isExporting: Bool = false

    @Published var actionInput: String = ""
    @Published var selectedStatus: OperationLogStatusFilter = .all
    @Published var startTimeInput: String = ""
    @Published var endTimeInput: String = ""
    @Published var includeAll: Bool = false

    @Published var page: Int = 1
    @Published var limit: Int = 30
    @Published var total: Int64 = 0

    @Published var errorMessage: String?
    @Published var successMessage: String?
    @Published var exportedFileURL: URL?

    private let service: AdminLogServiceProtocol

    init(service: AdminLogServiceProtocol = AdminLogService()) {
        self.service = service
    }

    func onAppear() {
        if logs.isEmpty {
            Task { await loadLogs(resetPage: true) }
        }
    }

    var canLoadMore: Bool {
        Int64(page * limit) < total
    }

    func refresh() async {
        await loadLogs(resetPage: true)
    }

    func loadNextPage() async {
        guard canLoadMore, !isLoading else { return }
        page += 1
        await loadLogs(resetPage: false)
    }

    func loadLogs(resetPage: Bool) async {
        isLoading = true
        errorMessage = nil
        if resetPage {
            successMessage = nil
            page = 1
        }
        defer { isLoading = false }

        do {
            let query = buildQuery(page: page)
            let response = try await service.listOperationLogs(query: query)
            total = response.pagination.total
            if resetPage {
                logs = response.items
            } else {
                logs.append(contentsOf: response.items)
            }
        } catch {
            errorMessage = "加载日志失败：\(error.localizedDescription)"
        }
    }

    func exportCSV() async {
        isExporting = true
        errorMessage = nil
        successMessage = nil
        defer { isExporting = false }

        do {
            var query = buildQuery(page: 1)
            query.limit = 5000
            let fileURL = try await service.exportOperationLogs(query: query)
            exportedFileURL = fileURL
            successMessage = "导出成功：\(fileURL.lastPathComponent)"
        } catch {
            errorMessage = "导出失败：\(error.localizedDescription)"
        }
    }

    private func buildQuery(page: Int) -> OperationLogQueryInput {
        OperationLogQueryInput(
            action: actionInput,
            status: selectedStatus.rawValue,
            startTime: startTimeInput,
            endTime: endTimeInput,
            page: page,
            limit: limit,
            includeAll: includeAll
        )
    }
}
