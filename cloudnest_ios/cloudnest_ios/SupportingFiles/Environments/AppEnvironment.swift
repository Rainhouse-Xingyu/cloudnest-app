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

    private static let overrideBaseURLKey = "network.overrideBaseURL"
    private static let lastExternalBaseURLKey = "network.lastExternalBaseURL"

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
        let baseURL = activeBaseURL(from: dict)
        let timeout = dict["REQUEST_TIMEOUT"] as? TimeInterval ?? 15

        return EnvironmentConfig(environment: env, apiBaseURL: baseURL, requestTimeout: timeout)
    }

    /// 保存用户手动选择的服务地址（例如真机联调时的内网 IP/外网域名）。
    static func saveOverrideBaseURL(_ value: String?) {
        let defaults = UserDefaults.standard
        let trimmed = value?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        if trimmed.isEmpty {
            defaults.removeObject(forKey: overrideBaseURLKey)
        } else {
            defaults.set(trimmed, forKey: overrideBaseURLKey)
        }
    }

    /// 保存最近一次有效的外网服务地址（用于登录页默认值与冷启动默认网络）。
    static func saveLastExternalBaseURL(_ value: String?) {
        let defaults = UserDefaults.standard
        let trimmed = value?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        if trimmed.isEmpty {
            defaults.removeObject(forKey: lastExternalBaseURLKey)
        } else {
            defaults.set(trimmed, forKey: lastExternalBaseURLKey)
        }
    }

    static func loadLastExternalBaseURL() -> String? {
        guard let raw = UserDefaults.standard.string(forKey: lastExternalBaseURLKey) else {
            return nil
        }
        let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.isEmpty ? nil : trimmed
    }

    private static func activeBaseURL(from dict: [String: Any]) -> String {
        if
            let override = UserDefaults.standard.string(forKey: overrideBaseURLKey),
            !override.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        {
            return override
        }

        if
            let external = loadLastExternalBaseURL(),
            !external.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        {
            return external
        }

        return dict["API_BASE_URL"] as? String ?? "http://127.0.0.1:8080"
    }
}
