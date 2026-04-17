import Foundation
import Combine

/// Settings 页面状态与交互逻辑
@MainActor
final class SettingsViewModel: ObservableObject {
    @Published var title: String = "加载中..."
    @Published var username: String = ""
    @Published var password: String = ""
    @Published var email: String = ""
    @Published var selectedRole: UserRoleOption = .user

    @Published var selectedMethod: PermissionMethodOption = .all
    @Published var permissionPathInput: String = ""
    @Published var permissions: [UserPermissionItem] = []

    @Published var isSubmitting: Bool = false
    @Published var isLoggingOut: Bool = false
    @Published var errorMessage: String?
    @Published var successMessage: String?

    private let service: SettingsServiceProtocol

    init(service: SettingsServiceProtocol = SettingsService()) {
        self.service = service
    }

    func onAppear() {
        title = service.loadTitle()
    }

    var canCreateUser: Bool {
        let cleanedUsername = username.trimmingCharacters(in: .whitespacesAndNewlines)
        let cleanedPassword = password.trimmingCharacters(in: .whitespacesAndNewlines)
        return !isSubmitting && !cleanedUsername.isEmpty && !cleanedPassword.isEmpty
    }

    var canAddPermission: Bool {
        !permissionPathInput.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    func addPermission() {
        let path = normalizePath(permissionPathInput)
        guard !path.isEmpty else { return }

        let exists = permissions.contains { $0.method == selectedMethod && $0.path == path }
        guard !exists else {
            errorMessage = "该权限已存在"
            return
        }

        permissions.append(UserPermissionItem(method: selectedMethod, path: path))
        permissionPathInput = ""
        errorMessage = nil
    }

    func removePermission(id: UUID) {
        permissions.removeAll { $0.id == id }
    }

    func createManagedUser() async {
        errorMessage = nil
        successMessage = nil

        let cleanedUsername = username.trimmingCharacters(in: .whitespacesAndNewlines)
        let cleanedPassword = password.trimmingCharacters(in: .whitespacesAndNewlines)
        let cleanedEmail = email.trimmingCharacters(in: .whitespacesAndNewlines)

        guard !cleanedUsername.isEmpty, !cleanedPassword.isEmpty else {
            errorMessage = "用户名和密码不能为空"
            return
        }

        isSubmitting = true
        defer { isSubmitting = false }

        do {
            let dto = CreateManagedUserRequestDTO(
                username: cleanedUsername,
                password: cleanedPassword,
                email: cleanedEmail,
                roleName: selectedRole.rawValue,
                permissions: permissions.map { item in
                    CreateManagedUserPermissionDTO(method: item.method.rawValue, path: item.path)
                }
            )

            let created = try await service.createManagedUser(dto)
            successMessage = "用户 \(created.username)（ID: \(created.id)）创建成功"
            resetFormForNextUser()
        } catch {
            errorMessage = "创建失败：\(error.localizedDescription)"
        }
    }

    func logout() async {
        errorMessage = nil
        successMessage = nil

        isLoggingOut = true
        defer { isLoggingOut = false }

        // 服务端登出失败不阻断本地登出，保证用户一定能退出。
        _ = try? await service.logout()

        SessionManager.shared.clear()
        NotificationCenter.default.post(name: .authLogout, object: nil)
    }

    private func resetFormForNextUser() {
        username = ""
        password = ""
        email = ""
        selectedRole = .user
        selectedMethod = .all
        permissionPathInput = ""
        permissions = []
    }

    private func normalizePath(_ input: String) -> String {
        let trimmed = input.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return "" }
        if trimmed.hasPrefix("/") {
            return trimmed
        }
        return "/" + trimmed
    }
}
