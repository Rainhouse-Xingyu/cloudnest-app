import Foundation

/// Settings 模块服务协议
protocol SettingsServiceProtocol {
    func loadTitle() -> String
    func createManagedUser(_ request: CreateManagedUserRequestDTO) async throws -> CreateManagedUserResponseDTO
    func createWorkspace(_ request: CreateWorkspaceRequestDTO) async throws
    func listWorkspaces() async throws -> [WorkspaceItemDTO]
    func listWorkspaceMembers(workspaceId: Int64) async throws -> [WorkspaceMemberDTO]
    func upsertWorkspaceMember(workspaceId: Int64, request: UpsertWorkspaceMemberRequestDTO) async throws
    func removeWorkspaceMember(workspaceId: Int64, userId: Int64) async throws
    func listFileCollaborators(fileId: Int64) async throws -> [FileCollaboratorDTO]
    func grantFileCollaborator(fileId: Int64, request: GrantFileCollaboratorRequestDTO) async throws
    func revokeFileCollaborator(fileId: Int64, userId: Int64) async throws
    func logout() async throws
}

/// Settings 模块服务默认实现
final class SettingsService: SettingsServiceProtocol {
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
        "设置"
    }

    func createManagedUser(_ request: CreateManagedUserRequestDTO) async throws -> CreateManagedUserResponseDTO {
        let body = try JSONEncoder().encode(request)
        let endpoint = AdminCreateUserEndpoint(body: body, token: try token)
        return try await networkClient.request(endpoint, decode: CreateManagedUserResponseDTO.self)
    }

    func createWorkspace(_ request: CreateWorkspaceRequestDTO) async throws {
        let body = try JSONEncoder().encode(request)
        let endpoint = GenericPostEndpoint(path: "/api/workspaces", body: body, token: try token)
        _ = try await networkClient.request(endpoint)
    }

    func listWorkspaces() async throws -> [WorkspaceItemDTO] {
        let endpoint = GenericGetEndpoint(path: "/api/workspaces", token: try token)
        let response = try await networkClient.request(endpoint, decode: WorkspaceListResponseDTO.self)
        return response.items
    }

    func listWorkspaceMembers(workspaceId: Int64) async throws -> [WorkspaceMemberDTO] {
        let endpoint = GenericGetEndpoint(path: "/api/workspaces/\(workspaceId)/members", token: try token)
        let response = try await networkClient.request(endpoint, decode: WorkspaceMemberListResponseDTO.self)
        return response.items
    }

    func upsertWorkspaceMember(workspaceId: Int64, request: UpsertWorkspaceMemberRequestDTO) async throws {
        let body = try JSONEncoder().encode(request)
        let endpoint = GenericPostEndpoint(path: "/api/workspaces/\(workspaceId)/members", body: body, token: try token)
        _ = try await networkClient.request(endpoint)
    }

    func removeWorkspaceMember(workspaceId: Int64, userId: Int64) async throws {
        let endpoint = GenericDeleteEndpoint(path: "/api/workspaces/\(workspaceId)/members/\(userId)", token: try token)
        _ = try await networkClient.request(endpoint)
    }

    func listFileCollaborators(fileId: Int64) async throws -> [FileCollaboratorDTO] {
        let endpoint = GenericGetEndpoint(path: "/api/files/\(fileId)/collaborators", token: try token)
        let response = try await networkClient.request(endpoint, decode: FileCollaboratorListResponseDTO.self)
        return response.items
    }

    func grantFileCollaborator(fileId: Int64, request: GrantFileCollaboratorRequestDTO) async throws {
        let body = try JSONEncoder().encode(request)
        let endpoint = GenericPostEndpoint(path: "/api/files/\(fileId)/collaborators", body: body, token: try token)
        _ = try await networkClient.request(endpoint)
    }

    func revokeFileCollaborator(fileId: Int64, userId: Int64) async throws {
        let endpoint = GenericDeleteEndpoint(path: "/api/files/\(fileId)/collaborators/\(userId)", token: try token)
        _ = try await networkClient.request(endpoint)
    }

    func logout() async throws {
        let endpoint = AuthLogoutEndpoint(token: try token)
        _ = try await networkClient.request(endpoint)
    }
}

private struct AdminCreateUserEndpoint: APIEndpoint {
    let body: Data?
    let token: String

    var path: String { "/api/admin/users" }
    var method: HTTPMethod { .post }
    var headers: [String: String] {
        [
            "Authorization": "Bearer \(token)",
            "Content-Type": "application/json",
            "Accept": "application/json"
        ]
    }
}

private struct AuthLogoutEndpoint: APIEndpoint {
    let token: String

    var path: String { "/api/auth/logout" }
    var method: HTTPMethod { .post }
    var headers: [String: String] {
        [
            "Authorization": "Bearer \(token)",
            "Accept": "application/json"
        ]
    }
}

private struct GenericPostEndpoint: APIEndpoint {
    let path: String
    let body: Data?
    let token: String

    var method: HTTPMethod { .post }
    var headers: [String: String] {
        [
            "Authorization": "Bearer \(token)",
            "Content-Type": "application/json",
            "Accept": "application/json"
        ]
    }
}

private struct GenericGetEndpoint: APIEndpoint {
    let path: String
    let token: String

    var method: HTTPMethod { .get }
    var headers: [String: String] {
        [
            "Authorization": "Bearer \(token)",
            "Accept": "application/json"
        ]
    }
}

private struct GenericDeleteEndpoint: APIEndpoint {
    let path: String
    let token: String

    var method: HTTPMethod { .delete }
    var headers: [String: String] {
        [
            "Authorization": "Bearer \(token)",
            "Accept": "application/json"
        ]
    }
}
