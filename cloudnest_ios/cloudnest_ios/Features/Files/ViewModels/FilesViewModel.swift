import Foundation
import Combine

/// Files 页面状态与交互逻辑
@MainActor
final class FilesViewModel: ObservableObject {
    @Published var title: String = "加载中..."

    private let service: FilesServiceProtocol

    init(service: FilesServiceProtocol = FilesService()) {
        self.service = service
    }

    func onAppear() {
        title = service.loadTitle()
    }
}
