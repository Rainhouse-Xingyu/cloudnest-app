import Foundation

/// 会话管理器：负责保存/读取登录 token 与基本用户信息
final class SessionManager {
    static let shared = SessionManager()

    private enum Keys {
        static let token = "auth.token"
        static let tokenType = "auth.tokenType"
        static let expiresAt = "auth.expiresAt"
        static let username = "auth.username"
        static let email = "auth.email"
    }

    private let defaults: UserDefaults

    private init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
    }

    func save(session: AuthSession) {
        defaults.set(session.token, forKey: Keys.token)
        defaults.set(session.tokenType, forKey: Keys.tokenType)
        defaults.set(session.expiresAt, forKey: Keys.expiresAt)
        defaults.set(session.username, forKey: Keys.username)
        defaults.set(session.email, forKey: Keys.email)
    }

    func loadSession() -> AuthSession? {
        guard
            let token = defaults.string(forKey: Keys.token),
            let tokenType = defaults.string(forKey: Keys.tokenType),
            let expiresAt = defaults.string(forKey: Keys.expiresAt),
            let username = defaults.string(forKey: Keys.username),
            let email = defaults.string(forKey: Keys.email)
        else {
            return nil
        }

        return AuthSession(
            token: token,
            tokenType: tokenType,
            expiresAt: expiresAt,
            username: username,
            email: email
        )
    }

    func clear() {
        defaults.removeObject(forKey: Keys.token)
        defaults.removeObject(forKey: Keys.tokenType)
        defaults.removeObject(forKey: Keys.expiresAt)
        defaults.removeObject(forKey: Keys.username)
        defaults.removeObject(forKey: Keys.email)
    }
}
