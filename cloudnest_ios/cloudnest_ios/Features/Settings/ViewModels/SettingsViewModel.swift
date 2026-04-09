import Foundation
import Combine

/// Settings 页面状态与交互逻辑
@MainActor
final class SettingsViewModel: ObservableObject {
    @Published var title: String = "加载中..."

    private let service: SettingsServiceProtocol

    init(service: SettingsServiceProtocol = SettingsService()) {
        self.service = service
    }

    func onAppear() {
        title = service.loadTitle()
    }
}
