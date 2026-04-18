import Foundation
import Combine
import Darwin

/// Auth 页面状态与交互逻辑
@MainActor
final class AuthViewModel: ObservableObject {
    @Published var username: String = ""
    @Published var password: String = ""
    @Published var useExternalConnection: Bool = false
    @Published var externalIP: String = ""
    @Published var isLoading: Bool = false
    @Published var isDetectingServer: Bool = false
    @Published var errorMessage: String?
    @Published var noticeMessage: String?
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
        noticeMessage = nil

        // 默认优先回填最近一次外网地址，避免回落到 127.0.0.1。
        if externalIP.isEmpty {
            if let lastExternal = EnvironmentConfig.loadLastExternalBaseURL() {
                externalIP = lastExternal
                useExternalConnection = true
            } else {
                externalIP = AppContainer.shared.networkClient.currentBaseURL()
            }
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
        noticeMessage = nil

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
                // 记录外网地址作为默认网络，避免后续启动回落到 127.0.0.1。
                EnvironmentConfig.saveLastExternalBaseURL(overrideBaseURL)

                let resolved = await resolveLANBaseURLIfPossible(
                    from: response.serverHosts,
                    fallback: overrideBaseURL
                )
                AppContainer.shared.networkClient.updateBaseURL(resolved)
                EnvironmentConfig.saveOverrideBaseURL(resolved)
                // 输入框保留外网地址，便于下次登录继续走外网入口。
                externalIP = overrideBaseURL
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
            if await isHealthReachable(baseURL: candidate, timeout: 1.2) {
                return candidate
            }
        }
        return fallback
    }

    /// 自动探测当前可达服务地址：先快速候选，再同网段扫描。
    func autoDetectReachableServer() async {
        errorMessage = nil
        noticeMessage = nil
        isDetectingServer = true
        defer { isDetectingServer = false }

        let base = useExternalConnection ? externalIP : AppContainer.shared.networkClient.currentBaseURL()
        let normalizedBase = normalizeExternalBaseURL(from: base) ?? AppContainer.shared.networkClient.currentBaseURL()
        let port = URL(string: normalizedBase)?.port ?? 8080

        let quickCandidates = buildQuickCandidates(port: port, base: normalizedBase)
        if let found = await firstReachableBaseURL(from: quickCandidates, timeout: 1.0) {
            applyDetectedBaseURL(found)
            return
        }

        let subnetCandidates = buildSubnetCandidates(port: port)
        if let found = await firstReachableBaseURL(from: subnetCandidates, timeout: 0.9) {
            applyDetectedBaseURL(found)
            return
        }

        errorMessage = "未探测到可达服务器，请确认手机与后端在同一局域网，端口为 \(port)"
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

    private func isHealthReachable(baseURL: String, timeout: TimeInterval = 1.5) async -> Bool {
        guard let url = URL(string: baseURL + "/api/resource/health") else {
            return false
        }

        var request = URLRequest(url: url)
        request.httpMethod = "GET"
        request.timeoutInterval = timeout

        do {
            let (_, response) = try await URLSession.shared.data(for: request)
            guard let http = response as? HTTPURLResponse else { return false }
            // health 接口在部分路由策略下会返回 401，但这代表服务可达。
            return (200...299).contains(http.statusCode) || http.statusCode == 401
        } catch {
            return false
        }
    }

    private func applyDetectedBaseURL(_ baseURL: String) {
        AppContainer.shared.networkClient.updateBaseURL(baseURL)
        EnvironmentConfig.saveOverrideBaseURL(baseURL)
        EnvironmentConfig.saveLastExternalBaseURL(baseURL)
        externalIP = baseURL
        useExternalConnection = true
        noticeMessage = "已探测到可达地址：\(baseURL)"
    }

    private func buildQuickCandidates(port: Int, base: String) -> [String] {
        var candidates: [String] = []

        let current = AppContainer.shared.networkClient.currentBaseURL().trimmingCharacters(in: .whitespacesAndNewlines)
        if !current.isEmpty {
            candidates.append(current)
        }

        if let normalized = normalizeExternalBaseURL(from: externalIP) {
            candidates.append(normalized)
        }

        if let last = EnvironmentConfig.loadLastExternalBaseURL() {
            candidates.append(last)
        }

        candidates.append(base)

        for ip in localIPv4Addresses() {
            candidates.append("http://\(ip):\(port)")
        }

        return deduplicated(candidates)
    }

    private func buildSubnetCandidates(port: Int) -> [String] {
        var candidates: [String] = []
        for ip in localIPv4Addresses() {
            let parts = ip.split(separator: ".")
            guard parts.count == 4 else { continue }
            let prefix = "\(parts[0]).\(parts[1]).\(parts[2])"
            let selfHost = Int(parts[3]) ?? 0

            // 先扫常见地址，再扫全段。
            let priorityHosts = [selfHost, 1, 2, 10, 100, 200].filter { $0 >= 1 && $0 <= 254 }
            for host in priorityHosts {
                candidates.append("http://\(prefix).\(host):\(port)")
            }
            for host in 1...254 {
                candidates.append("http://\(prefix).\(host):\(port)")
            }
        }
        return deduplicated(candidates)
    }

    private func firstReachableBaseURL(from candidates: [String], timeout: TimeInterval) async -> String? {
        guard !candidates.isEmpty else { return nil }

        return await withTaskGroup(of: String?.self) { group in
            for candidate in candidates {
                group.addTask {
                    if await self.isHealthReachable(baseURL: candidate, timeout: timeout) {
                        return candidate
                    }
                    return nil
                }
            }

            for await result in group {
                if let hit = result {
                    group.cancelAll()
                    return hit
                }
            }
            return nil
        }
    }

    private func deduplicated(_ values: [String]) -> [String] {
        var seen = Set<String>()
        return values.compactMap { value in
            let item = value.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !item.isEmpty else { return nil }
            guard !seen.contains(item) else { return nil }
            seen.insert(item)
            return item
        }
    }

    private func localIPv4Addresses() -> [String] {
        var result: [String] = []
        var addressPointer: UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&addressPointer) == 0, let first = addressPointer else {
            return []
        }
        defer { freeifaddrs(addressPointer) }

        var pointer = first
        while true {
            let interface = pointer.pointee
            let flags = Int32(interface.ifa_flags)
            let isUp = (flags & IFF_UP) == IFF_UP
            let isLoopback = (flags & IFF_LOOPBACK) == IFF_LOOPBACK

            if
                isUp,
                !isLoopback,
                let addr = interface.ifa_addr,
                addr.pointee.sa_family == UInt8(AF_INET)
            {
                var hostBuffer = [CChar](repeating: 0, count: Int(NI_MAXHOST))
                let len = socklen_t(addr.pointee.sa_len)
                if getnameinfo(addr, len, &hostBuffer, socklen_t(hostBuffer.count), nil, 0, NI_NUMERICHOST) == 0 {
                    let ip = String(cString: hostBuffer)
                    if ip.hasPrefix("192.168.") || ip.hasPrefix("10.") || ip.hasPrefix("172.") {
                        result.append(ip)
                    }
                }
            }

            guard let next = interface.ifa_next else { break }
            pointer = next
        }

        return deduplicated(result)
    }

    func logout() {
        sessionManager.clear()
        if let lastExternal = EnvironmentConfig.loadLastExternalBaseURL() {
            AppContainer.shared.networkClient.updateBaseURL(lastExternal)
            EnvironmentConfig.saveOverrideBaseURL(lastExternal)
            externalIP = lastExternal
            useExternalConnection = true
        }
        password = ""
        errorMessage = nil
        noticeMessage = nil
    }
}
