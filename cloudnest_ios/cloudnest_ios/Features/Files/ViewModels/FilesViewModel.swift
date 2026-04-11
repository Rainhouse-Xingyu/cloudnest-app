import Foundation
import Combine

/// Files 页面状态与交互逻辑
@MainActor
final class FilesViewModel: ObservableObject {
    @Published var title: String = "我的文件"
    @Published var folders: [FileItemDTO] = []
    @Published var files: [FileItemDTO] = []
    @Published var isLoading: Bool = false
    @Published var currentParentId: Int64 = 0
    @Published var errorMessage: String? = nil

    private let service: FilesServiceProtocol

    init(service: FilesServiceProtocol = FilesService()) {
        self.service = service
    }

    func onAppear() {
        Task {
            await loadData()
        }
    }
    
    func loadData(parentId: Int64? = nil) async {
        let pid = parentId ?? currentParentId
        isLoading = true
        errorMessage = nil
        do {
            let allItems = try await service.loadFiles(parentId: pid)
            self.folders = allItems.filter { $0.isFolder == 1 }
            self.files = allItems.filter { $0.isFolder == 0 }
            self.currentParentId = pid
        } catch {
            self.errorMessage = "加载失败: \(error.localizedDescription)"
        }
        isLoading = false
    }
}

