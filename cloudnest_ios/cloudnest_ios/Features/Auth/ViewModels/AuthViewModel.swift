import Foundation
import Combine

/// Auth 页面状态与交互逻辑
@MainActor
final class AuthViewModel: ObservableObject {
    @Published var title: String = "加载中..."

    private let service: AuthServiceProtocol

    init(service: AuthServiceProtocol = AuthService()) {
        self.service = service
    }

    func onAppear() {
        title = service.loadTitle()
    }
}
