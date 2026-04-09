import Foundation
import Combine

/// Profile 页面状态与交互逻辑
@MainActor
final class ProfileViewModel: ObservableObject {
    @Published var title: String = "加载中..."

    private let service: ProfileServiceProtocol

    init(service: ProfileServiceProtocol = ProfileService()) {
        self.service = service
    }

    func onAppear() {
        title = service.loadTitle()
    }
}
