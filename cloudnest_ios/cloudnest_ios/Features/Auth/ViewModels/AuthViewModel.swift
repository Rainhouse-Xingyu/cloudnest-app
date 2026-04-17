import Foundation
import Combine

/// Auth 页面状态与交互逻辑
@MainActor
final class AuthViewModel: ObservableObject {
    @Published var username: String = ""
    @Published var password: String = ""
    @Published var useExternalConnection: Bool = false
    @Published var externalIP: String = ""
    @Published var isLoading: Bool = false
    @Published var errorMessage: String?
    private let service: AuthServiceProtocol
    private let sessionManager: SessionManager

    init(
        service: AuthServiceProtocol = AuthService(),
        sessionManager: SessionManager = .shared
    ) {
        self.service = service
        self.sessionManager = sessionManager
    }

    func onAppear() {
        // 页面出现时仅清理错误态，不重置账号输入
        errorMessage = nil

        // 将当前全局服务地址回填到输入框，方便真机反复联调。
        if externalIP.isEmpty {
            externalIP = AppContainer.shared.networkClient.currentBaseURL()
        }
    }

    var canSubmit: Bool {
        let hasAccount = !username.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && !password.isEmpty
        guard hasAccount, !isLoading else { return false }

        // 勾选外网连接后，IP 输入不能为空
        if useExternalConnection {
            return !externalIP.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        }
        return true
    }

    /// 执行登录，成功返回 true，失败返回 false
    func login() async -> Bool {
        errorMessage = nil

        let cleanedUsername = username.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !cleanedUsername.isEmpty, !password.isEmpty else {
            errorMessage = "请输入用户名和密码"
            return false
        }

        var overrideBaseURL: String?
        if useExternalConnection {
            let cleanedIP = externalIP.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !cleanedIP.isEmpty else {
                errorMessage = "请输入外网 IP 地址"
                return false
            }

            guard let normalized = normalizeExternalBaseURL(from: cleanedIP) else {
                errorMessage = "IP 地址格式无效，请输入如 8.8.8.8:8080"
                return false
            }
            overrideBaseURL = normalized
        }

        isLoading = true
        defer { isLoading = false }

        do {
            let response = try await service.login(
                username: cleanedUsername,
                password: password,
                overrideBaseURL: overrideBaseURL
            )
            let saved = AuthSession(
                token: response.token,
                tokenType: response.tokenType,
                expiresAt: response.expiresAt,
                username: response.user.username,
                email: response.user.email
            )

            sessionManager.save(session: saved)

            if let overrideBaseURL, !overrideBaseURL.isEmpty {
                let resolved = await resolveLANBaseURLIfPossible(
                    from: response.serverHosts,
                    fallback: overrideBaseURL
                )
                AppContainer.shared.networkClient.updateBaseURL(resolved)
                EnvironmentConfig.saveOverrideBaseURL(resolved)
                externalIP = resolved
            }
            return true
        } catch {
            errorMessage = "登录失败：\(error.localizedDescription)"
            return false
        }
    }

    /// 将用户输入的 IP/域名归一化为 baseURL（自动补 http://）
    private func normalizeExternalBaseURL(from input: String) -> String? {
        var raw = input.trimmingCharacters(in: .whitespacesAndNewlines)
        if raw.isEmpty { return nil }

        if !raw.lowercased().hasPrefix("http://") && !raw.lowercased().hasPrefix("https://") {
            raw = "http://" + raw
        }

        guard let url = URL(string: raw), let scheme = url.scheme, let host = url.host, !host.isEmpty else {
            return nil
        }

        var normalized = "\(scheme)://\(host)"
        if let port = url.port {
            normalized += ":\(port)"
        }
        return normalized
    }

    /// 登录成功后优先探测局域网地址，命中后切换为局域网，失败则保留外网地址。
    private func resolveLANBaseURLIfPossible(from hosts: ServerHostsDTO?, fallback: String) async -> String {
        guard let base = URL(string: fallback) else {
            return fallback
        }

        let currentPort = base.port ?? extractPort(from: hosts?.requestHost) ?? 8080

        var candidates = [String]()

        if let hosts {
            // 优先使用后端回传的局域网 IP。
            for ip in hosts.lanIPs {
                guard !ip.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { continue }
                candidates.append("http://\(ip):\(currentPort)")
            }

            // request_host 可能是内网域名/IP，补充为候选。
            if
                let requestHost = hosts.requestHost,
                let normalizedHost = normalizeHostOnly(from: requestHost),
                !normalizedHost.isEmpty
            {
                candidates.append("http://\(normalizedHost):\(currentPort)")
            }
        }

        // 去重并过滤掉 fallback 本身。
        var seen = Set<String>()
        let ordered = candidates.filter { value in
            let item = value.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !item.isEmpty, item != fallback else { return false }
            if seen.contains(item) { return false }
            seen.insert(item)
            return true
        }

        for candidate in ordered {
            if await isHealthReachable(baseURL: candidate) {
                return candidate
            }
        }
        return fallback
    }

    private func extractPort(from host: String?) -> Int? {
        guard let host, !host.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
            return nil
        }
        if let url = URL(string: "http://\(host)"), let port = url.port {
            return port
        }
        return nil
    }

    private func normalizeHostOnly(from input: String) -> String? {
        let trimmed = input.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return nil }

        if let withScheme = URL(string: trimmed), let host = withScheme.host {
            return host
        }
        if let prefixed = URL(string: "http://\(trimmed)"), let host = prefixed.host {
            return host
        }
        return nil
    }

    private func isHealthReachable(baseURL: String) async -> Bool {
        guard let url = URL(string: baseURL + "/api/resource/health") else {
            return false
        }

        var request = URLRequest(url: url)
        request.httpMethod = "GET"
        request.timeoutInterval = 1.5

        do {
            let (_, response) = try await URLSession.shared.data(for: request)
            guard let http = response as? HTTPURLResponse else { return false }
            return (200...299).contains(http.statusCode)
        } catch {
            return false
        }
    }

    func logout() {
        sessionManager.clear()
        password = ""
        errorMessage = nil
    }
}
