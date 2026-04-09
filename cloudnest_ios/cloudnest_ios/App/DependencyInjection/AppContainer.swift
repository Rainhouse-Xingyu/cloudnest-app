import Foundation

/// 全局依赖容器：集中组装跨模块服务与客户端
final class AppContainer {
    static let shared = AppContainer()

    let environment: EnvironmentConfig
    let networkClient: NetworkClient

    private init() {
        let config = EnvironmentConfig.load()
        self.environment = config
        self.networkClient = URLSessionNetworkClient(
            baseURL: config.apiBaseURL,
            timeout: config.requestTimeout
        )
    }
}
