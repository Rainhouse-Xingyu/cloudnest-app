import Foundation

/// Files 模块服务协议，后续可对接真实 API
protocol FilesServiceProtocol {
    func loadTitle() -> String
}

/// Files 模块服务默认实现（占位）
final class FilesService: FilesServiceProtocol {
    func loadTitle() -> String {
        "Files 页面"
    }
}
