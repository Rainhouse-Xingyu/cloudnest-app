import Foundation
import Combine

/// Home 页面状态与交互逻辑
@MainActor
final class HomeViewModel: ObservableObject {
    @Published var title: String = "加载中..."

    private let service: HomeServiceProtocol

    init(service: HomeServiceProtocol = HomeService()) {
        self.service = service
    }

    func onAppear() {
        title = service.loadTitle()
    }
}
