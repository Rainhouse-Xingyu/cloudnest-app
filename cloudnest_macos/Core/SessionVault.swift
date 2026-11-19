import Foundation
import Security

struct SavedSession: Codable, Equatable, Sendable {
    let server: String
    let token: String
}

protocol SessionVault {
    func read() throws -> SavedSession?
    func write(_ session: SavedSession) throws
    func clear() throws
}

struct KeychainSessionVault: SessionVault {
    let service: String

    private var identity: [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: "active-session"
        ]
    }

    func read() throws -> SavedSession? {
        var query = identity
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &result)
        if status == errSecItemNotFound { return nil }
        try check(status)
        guard let data = result as? Data else { throw ServerError.invalidResponse }
        return try JSONDecoder().decode(SavedSession.self, from: data)
    }

    func write(_ session: SavedSession) throws {
        let data = try JSONEncoder().encode(session)
        let status = SecItemUpdate(identity as CFDictionary, [kSecValueData as String: data] as CFDictionary)
        if status != errSecItemNotFound {
            try check(status)
            return
        }
        var item = identity
        item[kSecValueData as String] = data
        try check(SecItemAdd(item as CFDictionary, nil))
    }

    func clear() throws {
        let status = SecItemDelete(identity as CFDictionary)
        if status != errSecItemNotFound { try check(status) }
    }

    private func check(_ status: OSStatus) throws {
        if status != errSecSuccess { throw KeychainError() }
    }
}

struct KeychainError: LocalizedError {
    var errorDescription: String? { "无法访问钥匙串。请解锁钥匙串后重试。" }
}
