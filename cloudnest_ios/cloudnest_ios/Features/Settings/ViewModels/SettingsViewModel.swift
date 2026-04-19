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
    @Published var isCollaborationLoading: Bool = false
    @Published var isRefreshingSummary: Bool = false
    @Published var isSystemSettingsLoading: Bool = false

    @Published var workspaceNameInput: String = ""
    @Published var workspaces: [WorkspaceItemDTO] = []
    @Published var selectedWorkspaceId: Int64?
    @Published var workspaceMembers: [WorkspaceMemberDTO] = []
    @Published var memberUsernameInput: String = ""
    @Published var selectedWorkspaceRole: WorkspaceRoleOption = .member
    @Published var removeMemberUserIdInput: String = ""

    @Published var targetFileIdInput: String = ""
    @Published var fileCollaborators: [FileCollaboratorDTO] = []
    @Published var collaboratorUsernameInput: String = ""
    @Published var selectedCollaboratorPermission: FileCollaboratorPermissionOption = .read
    @Published var revokeCollaboratorUserIdInput: String = ""

    @Published var permissionSummaryRoles: [PermissionRoleDTO] = []
    @Published var permissionSummaryUserRoles: [PermissionUserRoleDTO] = []
    @Published var rateWindowEnabled: Bool = false
    @Published var rateWindowStart: String = "09:00"
    @Published var rateWindowEnd: String = "18:00"
    @Published var rateWindowSource: String = ""
    @Published var editableSystemSettings: [EditableSystemSettingItem] = []

    @Published var errorMessage: String?
    @Published var successMessage: String?

    private let service: SettingsServiceProtocol

    init(service: SettingsServiceProtocol = SettingsService()) {
        self.service = service
    }

    func onAppear() {
        title = service.loadTitle()
        Task {
            await refreshAllSettingsData()
        }
    }

    func refreshAllSettingsData() async {
        await loadWorkspaces()
        await loadPermissionSummary()
        await loadRateWindow()
        await loadSystemSettings()
    }

    var canSaveSystemSettings: Bool {
        guard !isSystemSettingsLoading, !editableSystemSettings.isEmpty else { return false }
        return editableSystemSettings.allSatisfy {
            !$0.key.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty &&
            !$0.value.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        }
    }

    var canCreateUser: Bool {
        let cleanedUsername = username.trimmingCharacters(in: .whitespacesAndNewlines)
        let cleanedPassword = password.trimmingCharacters(in: .whitespacesAndNewlines)
        return !isSubmitting && !cleanedUsername.isEmpty && !cleanedPassword.isEmpty
    }

    var canAddPermission: Bool {
        let normalized = normalizePath(permissionPathInput)
        guard !normalized.isEmpty else { return false }
        return normalized.hasPrefix("/")
    }

    func addPermission() {
        let path = normalizePath(permissionPathInput)
        guard !path.isEmpty else { return }
        guard isValidPermissionPath(path) else {
            errorMessage = "路径格式不正确，请使用 /api/xxx"
            return
        }

        let exists = permissions.contains { $0.method == selectedMethod && $0.path == path }
        guard !exists else {
            errorMessage = "该权限已存在"
            return
        }

        permissions.append(UserPermissionItem(method: selectedMethod, path: path))
        permissionPathInput = ""
        errorMessage = nil
    }

    func loadPermissionSummary() async {
        isRefreshingSummary = true
        defer { isRefreshingSummary = false }

        do {
            let response = try await service.getPermissionSummary()
            permissionSummaryRoles = response.roles
            permissionSummaryUserRoles = response.userRoles
        } catch {
            errorMessage = "加载权限概览失败：\(error.localizedDescription)"
        }
    }

    func loadRateWindow() async {
        isRefreshingSummary = true
        defer { isRefreshingSummary = false }

        do {
            let item = try await service.getRateWindow()
            rateWindowEnabled = item.enabled
            rateWindowStart = item.start.isEmpty ? "09:00" : item.start
            rateWindowEnd = item.end.isEmpty ? "18:00" : item.end
            rateWindowSource = item.source
        } catch {
            errorMessage = "加载速率时段失败：\(error.localizedDescription)"
        }
    }

    func saveRateWindow() async {
        let start = rateWindowStart.trimmingCharacters(in: .whitespacesAndNewlines)
        let end = rateWindowEnd.trimmingCharacters(in: .whitespacesAndNewlines)

        if rateWindowEnabled {
            guard isValidHHMM(start), isValidHHMM(end) else {
                errorMessage = "时间格式需为 HH:MM，例如 09:30"
                return
            }
        }

        isRefreshingSummary = true
        defer { isRefreshingSummary = false }

        do {
            try await service.updateRateWindow(
                UpdateRateWindowRequestDTO(
                    enabled: rateWindowEnabled,
                    start: start,
                    end: end
                )
            )
            successMessage = "速率高峰时段已保存"
            await loadRateWindow()
        } catch {
            errorMessage = "保存速率时段失败：\(error.localizedDescription)"
        }
    }

    func loadSystemSettings() async {
        isSystemSettingsLoading = true
        defer { isSystemSettingsLoading = false }

        do {
            let rows = try await service.getSystemSettings()
            editableSystemSettings = rows
                .sorted { $0.settingKey < $1.settingKey }
                .map {
                    EditableSystemSettingItem(
                        key: $0.settingKey,
                        value: $0.settingValue,
                        description: $0.description
                    )
                }
        } catch {
            errorMessage = "加载系统设置失败：\(error.localizedDescription)"
        }
    }

    func saveSystemSettings() async {
        guard canSaveSystemSettings else {
            errorMessage = "存在空的设置项，请先补全"
            return
        }

        isSystemSettingsLoading = true
        defer { isSystemSettingsLoading = false }

        do {
            let payload = UpsertSystemSettingsRequestDTO(
                items: editableSystemSettings.map {
                    UpsertSystemSettingItemDTO(
                        settingKey: $0.key.trimmingCharacters(in: .whitespacesAndNewlines),
                        settingValue: $0.value.trimmingCharacters(in: .whitespacesAndNewlines),
                        description: $0.description.trimmingCharacters(in: .whitespacesAndNewlines)
                    )
                }
            )
            try await service.upsertSystemSettings(payload)
            successMessage = "系统设置已保存"
            await loadSystemSettings()
        } catch {
            errorMessage = "保存系统设置失败：\(error.localizedDescription)"
        }
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

    func loadWorkspaces() async {
        isCollaborationLoading = true
        defer { isCollaborationLoading = false }

        do {
            let rows = try await service.listWorkspaces()
            workspaces = rows

            if let selectedWorkspaceId, rows.contains(where: { $0.id == selectedWorkspaceId }) {
                await loadWorkspaceMembers()
            } else if let first = rows.first {
                selectedWorkspaceId = first.id
                await loadWorkspaceMembers()
            } else {
                selectedWorkspaceId = nil
                workspaceMembers = []
            }
        } catch {
            errorMessage = "加载工作空间失败：\(error.localizedDescription)"
        }
    }

    func createWorkspace() async {
        let name = workspaceNameInput.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !name.isEmpty else {
            errorMessage = "工作空间名称不能为空"
            return
        }

        isCollaborationLoading = true
        defer { isCollaborationLoading = false }

        do {
            try await service.createWorkspace(CreateWorkspaceRequestDTO(name: name))
            workspaceNameInput = ""
            successMessage = "工作空间创建成功"
            await loadWorkspaces()
        } catch {
            errorMessage = "创建工作空间失败：\(error.localizedDescription)"
        }
    }

    func selectWorkspace(_ id: Int64) async {
        selectedWorkspaceId = id
        await loadWorkspaceMembers()
    }

    func loadWorkspaceMembers() async {
        guard let workspaceId = selectedWorkspaceId else {
            workspaceMembers = []
            return
        }

        isCollaborationLoading = true
        defer { isCollaborationLoading = false }

        do {
            workspaceMembers = try await service.listWorkspaceMembers(workspaceId: workspaceId)
        } catch {
            errorMessage = "加载成员失败：\(error.localizedDescription)"
        }
    }

    func upsertWorkspaceMember() async {
        guard let workspaceId = selectedWorkspaceId else {
            errorMessage = "请先选择工作空间"
            return
        }
        let username = memberUsernameInput.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !username.isEmpty else {
            errorMessage = "成员用户名不能为空"
            return
        }

        isCollaborationLoading = true
        defer { isCollaborationLoading = false }

        do {
            try await service.upsertWorkspaceMember(
                workspaceId: workspaceId,
                request: UpsertWorkspaceMemberRequestDTO(username: username, role: selectedWorkspaceRole.rawValue)
            )
            memberUsernameInput = ""
            successMessage = "成员已添加/更新"
            await loadWorkspaceMembers()
        } catch {
            errorMessage = "更新成员失败：\(error.localizedDescription)"
        }
    }

    func removeWorkspaceMember() async {
        guard let workspaceId = selectedWorkspaceId else {
            errorMessage = "请先选择工作空间"
            return
        }
        guard let targetUserId = Int64(removeMemberUserIdInput.trimmingCharacters(in: .whitespacesAndNewlines)), targetUserId > 0 else {
            errorMessage = "请输入正确的成员用户 ID"
            return
        }

        isCollaborationLoading = true
        defer { isCollaborationLoading = false }

        do {
            try await service.removeWorkspaceMember(workspaceId: workspaceId, userId: targetUserId)
            removeMemberUserIdInput = ""
            successMessage = "成员移除成功"
            await loadWorkspaceMembers()
        } catch {
            errorMessage = "移除成员失败：\(error.localizedDescription)"
        }
    }

    func loadFileCollaborators() async {
        guard let fileId = Int64(targetFileIdInput.trimmingCharacters(in: .whitespacesAndNewlines)), fileId > 0 else {
            errorMessage = "请输入正确的文件 ID"
            return
        }

        isCollaborationLoading = true
        defer { isCollaborationLoading = false }

        do {
            fileCollaborators = try await service.listFileCollaborators(fileId: fileId)
        } catch {
            errorMessage = "加载文件协作者失败：\(error.localizedDescription)"
        }
    }

    func grantFileCollaborator() async {
        guard let fileId = Int64(targetFileIdInput.trimmingCharacters(in: .whitespacesAndNewlines)), fileId > 0 else {
            errorMessage = "请输入正确的文件 ID"
            return
        }
        let username = collaboratorUsernameInput.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !username.isEmpty else {
            errorMessage = "协作者用户名不能为空"
            return
        }

        isCollaborationLoading = true
        defer { isCollaborationLoading = false }

        do {
            try await service.grantFileCollaborator(
                fileId: fileId,
                request: GrantFileCollaboratorRequestDTO(username: username, permission: selectedCollaboratorPermission.rawValue)
            )
            collaboratorUsernameInput = ""
            successMessage = "文件协作权限已更新"
            await loadFileCollaborators()
        } catch {
            errorMessage = "授予协作权限失败：\(error.localizedDescription)"
        }
    }

    func revokeFileCollaborator() async {
        guard let fileId = Int64(targetFileIdInput.trimmingCharacters(in: .whitespacesAndNewlines)), fileId > 0 else {
            errorMessage = "请输入正确的文件 ID"
            return
        }
        guard let targetUserId = Int64(revokeCollaboratorUserIdInput.trimmingCharacters(in: .whitespacesAndNewlines)), targetUserId > 0 else {
            errorMessage = "请输入正确的协作者用户 ID"
            return
        }

        isCollaborationLoading = true
        defer { isCollaborationLoading = false }

        do {
            try await service.revokeFileCollaborator(fileId: fileId, userId: targetUserId)
            revokeCollaboratorUserIdInput = ""
            successMessage = "协作者权限已撤销"
            await loadFileCollaborators()
        } catch {
            errorMessage = "撤销协作权限失败：\(error.localizedDescription)"
        }
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

    private func isValidPermissionPath(_ path: String) -> Bool {
        let pattern = "^/[A-Za-z0-9_\\-./{}:]*$"
        return path.range(of: pattern, options: .regularExpression) != nil
    }

    private func isValidHHMM(_ value: String) -> Bool {
        let pattern = "^([01][0-9]|2[0-3]):[0-5][0-9]$"
        return value.range(of: pattern, options: .regularExpression) != nil
    }
}
