import Foundation

/// Settings 模块服务协议，后续可对接真实 API
protocol SettingsServiceProtocol {
    func loadTitle() -> String
}

/// Settings 模块服务默认实现（占位）
final class SettingsService: SettingsServiceProtocol {
    func loadTitle() -> String {
        "Settings 页面"
    }
}
