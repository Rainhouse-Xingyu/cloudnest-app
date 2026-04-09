import Foundation

/// Auth 模块服务协议，后续可对接真实 API
protocol AuthServiceProtocol {
    func loadTitle() -> String
}

/// Auth 模块服务默认实现（占位）
final class AuthService: AuthServiceProtocol {
    func loadTitle() -> String {
        "Auth 页面"
    }
}
