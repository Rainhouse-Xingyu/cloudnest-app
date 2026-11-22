import Foundation

struct FRPDeviceSnapshot: Encodable, Sendable {
    let device_name: String
    let device_ip: String
    let status: Int
    init(_ device: FRPDevice) { device_name = device.name; device_ip = device.address; status = device.status }
}

struct FRPDeviceArchive: Decodable, Sendable {
    let device: FRPDevice
    let mappingCount: Int64
    let latestConfigTime: String?
    enum CodingKeys: String, CodingKey { case device; case mappingCount = "mapping_count", latestConfigTime = "latest_config_time" }
}

extension ServerAPI {
    func writeFRPDevice(_ origin: ServerOrigin, token: String, original: FRPDevice?, name: String, address: String, enabled: Bool) async throws -> FRPDevice {
        struct Input: Encodable {
            let device_name: String
            let device_ip: String
            let status: Int?
            let clear_device_ip: Bool?
            let expected: FRPDeviceSnapshot?
        }
        struct Response: Decodable { let item: FRPDevice }
        let cleanName = name.trimmingCharacters(in: .whitespacesAndNewlines)
        let cleanAddress = address.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !cleanName.isEmpty, cleanName.unicodeScalars.count <= 100, cleanName.utf8.count <= 400,
              cleanAddress.unicodeScalars.count <= 50, cleanAddress.utf8.count <= 200,
              cleanName.rangeOfCharacter(from: .controlCharacters) == nil, cleanAddress.rangeOfCharacter(from: .controlCharacters) == nil,
              original.map({ $0.id > 0 && !$0.isRetired }) ?? true else { throw ServerError.invalidOperation }
        let input = Input(device_name: cleanName, device_ip: cleanAddress, status: original == nil ? nil : enabled ? 1 : 0,
            clear_device_ip: original.map { !$0.address.isEmpty && cleanAddress.isEmpty }, expected: original.map(FRPDeviceSnapshot.init))
        let result: Response = try await request(origin, original.map { "api/frp/devices/\($0.id)" } ?? "api/frp/devices",
            token: token, body: try JSONEncoder().encode(input), method: original == nil ? "POST" : "PUT", maximumBytes: 64 * 1024)
        try result.item.validate(retired: false)
        guard original.map({ result.item.id == $0.id && result.item.userID == $0.userID }) ?? true,
              result.item.name == cleanName, result.item.address == cleanAddress,
              result.item.status == (original == nil || enabled ? 1 : 0) else { throw ServerError.invalidResponse }
        return result.item
    }

    func retireFRPDevice(_ origin: ServerOrigin, token: String, id: Int64) async throws {
        struct Receipt: Decodable { let retired: Bool; let reservations_retained: Bool }
        guard id > 0 else { throw ServerError.invalidOperation }
        let result: Receipt = try await request(origin, "api/frp/devices/\(id)", token: token, method: "DELETE", maximumBytes: 64 * 1024)
        guard result.retired, result.reservations_retained else { throw ServerError.invalidResponse }
    }

    func frpDeviceArchive(_ origin: ServerOrigin, token: String, id: Int64) async throws -> FRPDeviceArchive {
        struct Response: Decodable { let item: FRPDeviceArchive }
        guard id > 0 else { throw ServerError.invalidOperation }
        let result: Response = try await request(origin, "api/frp/devices/\(id)", token: token,
            query: [URLQueryItem(name: "retired", value: "1")], maximumBytes: 64 * 1024)
        try result.item.device.validate(retired: true)
        guard result.item.device.id == id, result.item.mappingCount >= 0 else { throw ServerError.invalidResponse }
        return result.item
    }
}
