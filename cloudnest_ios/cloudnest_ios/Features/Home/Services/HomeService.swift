import Foundation

/// Home 模块服务协议
protocol HomeServiceProtocol {
    func loadTitle() -> String
    func fetchDashboard() async throws -> HomeDashboardDTO
}

/// Home 模块服务默认实现
final class HomeService: HomeServiceProtocol {
    private let networkClient: NetworkClient

    init(networkClient: NetworkClient = AppContainer.shared.networkClient) {
        self.networkClient = networkClient
    }

    private var token: String {
        get throws {
            guard let tk = SessionManager.shared.loadSession()?.token, !tk.isEmpty else {
                throw NetworkError.invalidResponse
            }
            return tk
        }
    }

    func loadTitle() -> String {
        "首页"
    }

    func fetchDashboard() async throws -> HomeDashboardDTO {
        let authToken = try token

        async let fileResponse: HomeFilesResponseDTO = networkClient.request(
            HomeFilesEndpoint(parentId: 0, token: authToken),
            decode: HomeFilesResponseDTO.self
        )
        async let livePhotoResponse: HomeLivePhotosResponseDTO = networkClient.request(
            HomeLivePhotosEndpoint(page: 1, limit: 12, token: authToken),
            decode: HomeLivePhotosResponseDTO.self
        )
        async let frpResponse: HomeFrpDevicesResponseDTO = networkClient.request(
            HomeFrpDevicesEndpoint(token: authToken),
            decode: HomeFrpDevicesResponseDTO.self
        )
        async let rateWindowResponse: HomeRateWindowResponseDTO = networkClient.request(
            HomeRateWindowEndpoint(token: authToken),
            decode: HomeRateWindowResponseDTO.self
        )
        // 容量概览接口失败时不阻断首页其他信息展示。
        async let storageOverviewResponse: HomeStorageOverviewResponseDTO? = try? networkClient.request(
            HomeStorageOverviewEndpoint(token: authToken),
            decode: HomeStorageOverviewResponseDTO.self
        )

        return try await HomeDashboardDTO(
            files: fileResponse,
            livePhotos: livePhotoResponse,
            frpDevices: frpResponse,
            rateWindow: rateWindowResponse,
            storageOverview: storageOverviewResponse
        )
    }
}

// MARK: - Dashboard DTO

struct HomeDashboardDTO {
    let files: HomeFilesResponseDTO
    let livePhotos: HomeLivePhotosResponseDTO
    let frpDevices: HomeFrpDevicesResponseDTO
    let rateWindow: HomeRateWindowResponseDTO
    let storageOverview: HomeStorageOverviewResponseDTO?
}

// MARK: - Files DTO

struct HomeFilesResponseDTO: Decodable {
    let items: [HomeFileDTO]
}

struct HomeFileDTO: Decodable {
    let id: Int64
    let filename: String
    let isFolder: Int8
    let size: Int64
    let updateTime: String

    enum CodingKeys: String, CodingKey {
        case id
        case filename
        case isFolder = "is_folder"
        case size
        case updateTime = "update_time"
    }
}

// MARK: - Live Photo DTO

struct HomeLivePhotosResponseDTO: Decodable {
    let items: [HomeLivePhotoDTO]
}

struct HomeLivePhotoDTO: Decodable {
    let id: Int64
    let imageFileID: Int64
    let videoFileID: Int64
    let createTime: String
    let imageFile: HomeSimpleFileDTO
    let videoFile: HomeSimpleFileDTO

    enum CodingKeys: String, CodingKey {
        case id
        case imageFileID = "image_file_id"
        case videoFileID = "video_file_id"
        case createTime = "create_time"
        case imageFile = "image_file"
        case videoFile = "video_file"
    }
}

struct HomeSimpleFileDTO: Decodable {
    let id: Int64
    let filename: String
}

// MARK: - FRP DTO

struct HomeFrpDevicesResponseDTO: Decodable {
    let items: [HomeFrpDeviceDTO]
}

struct HomeFrpDeviceDTO: Decodable {
    let id: Int64
    let status: Int8
}

// MARK: - Settings DTO

struct HomeRateWindowResponseDTO: Decodable {
    let item: HomeRateWindowItemDTO
}

struct HomeRateWindowItemDTO: Decodable {
    let enabled: Bool
    let start: String
    let end: String
}

// MARK: - Storage DTO

struct HomeStorageOverviewResponseDTO: Decodable {
    let item: HomeStorageOverviewItemDTO
}

struct HomeStorageOverviewItemDTO: Decodable {
    let usedBytes: Int64
    let totalBytes: Int64
    let remainingBytes: Int64
    let usagePercent: Double
    let hasQuota: Bool
    let quotaSource: String

    enum CodingKeys: String, CodingKey {
        case usedBytes = "used_bytes"
        case totalBytes = "total_bytes"
        case remainingBytes = "remaining_bytes"
        case usagePercent = "usage_percent"
        case hasQuota = "has_quota"
        case quotaSource = "quota_source"
    }
}

// MARK: - Endpoints

private struct HomeFilesEndpoint: APIEndpoint {
    let parentId: Int64
    let token: String

    var path: String { "/api/files" }
    var method: HTTPMethod { .get }
    var queryItems: [URLQueryItem] { [URLQueryItem(name: "parent_id", value: "\(parentId)")] }
    var headers: [String : String] {
        [
            "Authorization": "Bearer \(token)",
            "Accept": "application/json"
        ]
    }
}

private struct HomeLivePhotosEndpoint: APIEndpoint {
    let page: Int
    let limit: Int
    let token: String

    var path: String { "/api/live-photos" }
    var method: HTTPMethod { .get }
    var queryItems: [URLQueryItem] {
        [
            URLQueryItem(name: "page", value: "\(page)"),
            URLQueryItem(name: "limit", value: "\(limit)")
        ]
    }
    var headers: [String : String] {
        [
            "Authorization": "Bearer \(token)",
            "Accept": "application/json"
        ]
    }
}

private struct HomeFrpDevicesEndpoint: APIEndpoint {
    let token: String

    var path: String { "/api/frp/devices" }
    var method: HTTPMethod { .get }
    var headers: [String : String] {
        [
            "Authorization": "Bearer \(token)",
            "Accept": "application/json"
        ]
    }
}

private struct HomeRateWindowEndpoint: APIEndpoint {
    let token: String

    var path: String { "/api/settings/rate-window" }
    var method: HTTPMethod { .get }
    var headers: [String : String] {
        [
            "Authorization": "Bearer \(token)",
            "Accept": "application/json"
        ]
    }
}

private struct HomeStorageOverviewEndpoint: APIEndpoint {
    let token: String

    var path: String { "/api/storage/overview" }
    var method: HTTPMethod { .get }
    var headers: [String : String] {
        [
            "Authorization": "Bearer \(token)",
            "Accept": "application/json"
        ]
    }
}

