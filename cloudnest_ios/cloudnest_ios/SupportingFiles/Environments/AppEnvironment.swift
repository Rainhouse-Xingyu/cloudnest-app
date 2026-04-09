import Foundation

/// 运行环境定义
enum AppEnvironment: String {
    case dev
    case test
    case prod
}

/// 环境配置读取器
struct EnvironmentConfig {
    let environment: AppEnvironment
    let apiBaseURL: String
    let requestTimeout: TimeInterval

    /// 从 Environment.plist 加载配置，缺省走安全默认值
    static func load(bundle: Bundle = .main) -> EnvironmentConfig {
        guard
            let url = bundle.url(forResource: "Environment", withExtension: "plist"),
            let data = try? Data(contentsOf: url),
            let object = try? PropertyListSerialization.propertyList(from: data, format: nil),
            let dict = object as? [String: Any]
        else {
            return EnvironmentConfig(environment: .dev, apiBaseURL: "http://127.0.0.1:8080", requestTimeout: 15)
        }

        let envRaw = (dict["APP_ENV"] as? String ?? "dev").lowercased()
        let env = AppEnvironment(rawValue: envRaw) ?? .dev
        let baseURL = dict["API_BASE_URL"] as? String ?? "http://127.0.0.1:8080"
        let timeout = dict["REQUEST_TIMEOUT"] as? TimeInterval ?? 15

        return EnvironmentConfig(environment: env, apiBaseURL: baseURL, requestTimeout: timeout)
    }
}
