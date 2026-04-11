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

    func logout() {
        sessionManager.clear()
        password = ""
        errorMessage = nil
    }
}
