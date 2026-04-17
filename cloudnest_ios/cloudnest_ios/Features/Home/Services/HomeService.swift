import Foundation

/// Home 模块服务协议，后续可对接真实 API
protocol HomeServiceProtocol {
    func loadTitle() -> String
}

/// Home 模块服务默认实现（占位）
final class HomeService: HomeServiceProtocol {
    func loadTitle() -> String {
        "首页"
    }
}
