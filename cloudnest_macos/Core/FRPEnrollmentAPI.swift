import Foundation

// One-time codes live only in the presenting state. Never make this Codable
// or place it in the command journal, defaults, logs, or the session vault.
struct FRPEnrollment: Sendable {
    let deviceID: Int64
    let code: String
    let expiresAt: Date
}

extension ServerAPI {
    func issueFRPEnrollment(_ origin: ServerOrigin, token: String, deviceID: Int64, password: String) async throws -> FRPEnrollment {
        struct Input: Encodable { let password: String }
        struct Receipt: Decodable { let device_id: Int64; let enrollment: String; let expires_at: String }
        try validateFRPReauthentication(deviceID: deviceID, password: password)
        let result: Receipt
        do {
            result = try await request(origin, "api/frp/devices/\(deviceID)/enrollment", token: token,
                body: try JSONEncoder().encode(Input(password: password)), method: "POST", maximumBytes: 4096, successStatus: 201)
        } catch ServerError.unauthorized {
            // This endpoint also uses 401 for a wrong current password. Check
            // the original JWT, never a later login, before preserving it.
            _ = try await profile(origin, token: token)
            throw ServerError.reauthenticationRejected
        }
        let pieces = result.enrollment.split(separator: ".", omittingEmptySubsequences: false)
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        let expiry = formatter.date(from: result.expires_at) ?? ISO8601DateFormatter().date(from: result.expires_at)
        let secret = pieces.last.map(String.init) ?? ""
        let decodedSecret = Data(base64Encoded: secret.replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/") + "=")
        guard result.device_id == deviceID, pieces.count == 3, pieces[0] == "cne1", pieces[1].utf8.count == 32,
              pieces[1].utf8.allSatisfy({ (48...57).contains($0) || (97...102).contains($0) }),
              pieces[2].utf8.count == 43,
              pieces[2].utf8.allSatisfy({ (48...57).contains($0) || (65...90).contains($0) || (97...122).contains($0) || $0 == 45 || $0 == 95 }),
              let decodedSecret, decodedSecret.count == 32,
              decodedSecret.base64EncodedString().replacingOccurrences(of: "+", with: "-").replacingOccurrences(of: "/", with: "_").replacingOccurrences(of: "=", with: "") == secret,
              let expiry, expiry > Date(), expiry.timeIntervalSinceNow <= 15 * 60 else { throw ServerError.invalidResponse }
        return FRPEnrollment(deviceID: deviceID, code: result.enrollment, expiresAt: expiry)
    }

    func revokeFRPCredential(_ origin: ServerOrigin, token: String, deviceID: Int64, password: String) async throws {
        struct Input: Encodable { let password: String }
        try validateFRPReauthentication(deviceID: deviceID, password: password)
        do {
            let _: EmptyResponse = try await request(origin, "api/frp/devices/\(deviceID)/credential/revoke", token: token,
                body: try JSONEncoder().encode(Input(password: password)), method: "POST", maximumBytes: 4096, successStatus: 204)
        } catch ServerError.unauthorized {
            _ = try await profile(origin, token: token)
            throw ServerError.reauthenticationRejected
        }
    }

    private func validateFRPReauthentication(deviceID: Int64, password: String) throws {
        guard deviceID > 0, !password.isEmpty, password.utf8.count <= 72 else { throw ServerError.invalidOperation }
    }
}
