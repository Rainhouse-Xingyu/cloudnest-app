import Foundation

/// Profile 模块服务协议，后续可对接真实 API
protocol ProfileServiceProtocol {
    func loadTitle() -> String
}

/// Profile 模块服务默认实现（占位）
final class ProfileService: ProfileServiceProtocol {
    func loadTitle() -> String {
        "Profile 页面"
    }
}
