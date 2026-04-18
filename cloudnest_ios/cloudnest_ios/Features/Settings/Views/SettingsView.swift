import SwiftUI

/// 设置页：账户管理与退出登录
struct SettingsView: View {
    @StateObject private var viewModel = SettingsViewModel()

    var body: some View {
        ScrollView(showsIndicators: false) {
            VStack(alignment: .leading, spacing: 16) {
                headerCard
                accountFormCard
                permissionCard
                createButton
                collaborationWorkspaceCard
                collaborationFilePermissionCard
                statusCard

                // 页面底部退出按钮
                logoutButton
                    .padding(.top, 8)
                    .padding(.bottom, 24)
            }
            .padding(.horizontal, 16)
            .padding(.top, 16)
        }
        .liquidGlassPage()
        .onAppear {
            viewModel.onAppear()
        }
    }

    private var headerCard: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(viewModel.title)
                .font(.system(size: 28, weight: .bold, design: .rounded))
            Text("账户管理")
                .font(.system(size: 15, weight: .medium, design: .rounded))
                .foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(16)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 18, style: .continuous)
                .stroke(Color.white.opacity(0.6), lineWidth: 1)
        )
    }

    private var accountFormCard: some View {
        VStack(alignment: .leading, spacing: 12) {
            sectionTitle("添加新用户")
            inputField(title: "用户名", text: $viewModel.username)
            secureInputField(title: "密码", text: $viewModel.password)
            inputField(title: "邮箱（可选）", text: $viewModel.email)

            VStack(alignment: .leading, spacing: 8) {
                Text("角色分配")
                    .font(.system(size: 14, weight: .semibold))

                Picker("角色", selection: $viewModel.selectedRole) {
                    ForEach(UserRoleOption.allCases) { role in
                        Text(role.title).tag(role)
                    }
                }
                .pickerStyle(.segmented)
            }
        }
        .padding(16)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 18, style: .continuous)
                .stroke(Color.white.opacity(0.6), lineWidth: 1)
        )
    }

    private var permissionCard: some View {
        VStack(alignment: .leading, spacing: 12) {
            sectionTitle("访问路径权限")

            Picker("方法", selection: $viewModel.selectedMethod) {
                ForEach(PermissionMethodOption.allCases) { method in
                    Text(method.rawValue).tag(method)
                }
            }
            .pickerStyle(.segmented)

            HStack(spacing: 8) {
                TextField("输入路径，如 /api/files", text: $viewModel.permissionPathInput)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled(true)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 10)
                    .background(Color.white.opacity(0.35), in: RoundedRectangle(cornerRadius: 10, style: .continuous))

                Button("添加") {
                    viewModel.addPermission()
                }
                .buttonStyle(.borderedProminent)
                .disabled(!viewModel.canAddPermission)
            }

            if viewModel.permissions.isEmpty {
                Text("未配置权限时，用户仍可登录，但仅拥有后端默认权限。")
                    .font(.system(size: 12))
                    .foregroundStyle(.secondary)
            } else {
                VStack(alignment: .leading, spacing: 8) {
                    ForEach(viewModel.permissions) { item in
                        HStack {
                            Text("\(item.method.rawValue)  \(item.path)")
                                .font(.system(size: 13, weight: .medium, design: .monospaced))
                                .lineLimit(1)
                            Spacer()
                            Button(role: .destructive) {
                                viewModel.removePermission(id: item.id)
                            } label: {
                                Image(systemName: "trash")
                            }
                            .buttonStyle(.plain)
                        }
                        .padding(.horizontal, 10)
                        .padding(.vertical, 8)
                        .background(Color.white.opacity(0.28), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                    }
                }
            }
        }
        .padding(16)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 18, style: .continuous)
                .stroke(Color.white.opacity(0.6), lineWidth: 1)
        )
    }

    private var createButton: some View {
        Button {
            Task {
                await viewModel.createManagedUser()
            }
        } label: {
            HStack {
                if viewModel.isSubmitting {
                    ProgressView().tint(.white)
                }
                Text(viewModel.isSubmitting ? "创建中..." : "创建用户")
                    .font(.system(size: 16, weight: .bold, design: .rounded))
            }
            .frame(maxWidth: .infinity)
            .padding(.vertical, 14)
            .background(
                LinearGradient(
                    colors: [.blue, .cyan],
                    startPoint: .leading,
                    endPoint: .trailing
                ),
                in: RoundedRectangle(cornerRadius: 12, style: .continuous)
            )
            .foregroundStyle(.white)
        }
        .buttonStyle(.plain)
        .disabled(!viewModel.canCreateUser)
        .opacity(viewModel.canCreateUser ? 1 : 0.6)
    }

    private var collaborationWorkspaceCard: some View {
        VStack(alignment: .leading, spacing: 12) {
            sectionTitle("协作空间")

            HStack(spacing: 8) {
                TextField("工作空间名称", text: $viewModel.workspaceNameInput)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled(true)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 10)
                    .background(Color.white.opacity(0.35), in: RoundedRectangle(cornerRadius: 10, style: .continuous))

                Button("创建") {
                    Task { await viewModel.createWorkspace() }
                }
                .buttonStyle(.borderedProminent)
            }

            if !viewModel.workspaces.isEmpty {
                Picker("选择空间", selection: Binding(
                    get: { viewModel.selectedWorkspaceId ?? 0 },
                    set: { newValue in
                        Task { await viewModel.selectWorkspace(newValue) }
                    }
                )) {
                    ForEach(viewModel.workspaces) { item in
                        Text("\(item.name) (\(item.role))").tag(item.id)
                    }
                }
                .pickerStyle(.menu)
            } else {
                Text("暂无工作空间")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            }

            HStack(spacing: 8) {
                TextField("成员用户名", text: $viewModel.memberUsernameInput)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled(true)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 10)
                    .background(Color.white.opacity(0.35), in: RoundedRectangle(cornerRadius: 10, style: .continuous))

                Picker("角色", selection: $viewModel.selectedWorkspaceRole) {
                    ForEach(WorkspaceRoleOption.allCases) { role in
                        Text(role.title).tag(role)
                    }
                }
                .pickerStyle(.menu)

                Button("保存") {
                    Task { await viewModel.upsertWorkspaceMember() }
                }
                .buttonStyle(.borderedProminent)
            }

            HStack(spacing: 8) {
                TextField("移除成员 user_id", text: $viewModel.removeMemberUserIdInput)
                    .keyboardType(.numberPad)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 10)
                    .background(Color.white.opacity(0.35), in: RoundedRectangle(cornerRadius: 10, style: .continuous))

                Button("移除") {
                    Task { await viewModel.removeWorkspaceMember() }
                }
                .buttonStyle(.bordered)
                .tint(.red)
            }

            if viewModel.isCollaborationLoading {
                ProgressView("协作数据加载中...")
                    .font(.footnote)
            }

            if viewModel.workspaceMembers.isEmpty {
                Text("暂无成员数据")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            } else {
                VStack(alignment: .leading, spacing: 6) {
                    ForEach(viewModel.workspaceMembers) { item in
                        Text("#\(item.userId)  \(item.username)  (\(item.role))")
                            .font(.system(size: 13, weight: .medium, design: .monospaced))
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(.horizontal, 10)
                            .padding(.vertical, 8)
                            .background(Color.white.opacity(0.28), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                    }
                }
            }
        }
        .padding(16)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 18, style: .continuous)
                .stroke(Color.white.opacity(0.6), lineWidth: 1)
        )
    }

    private var collaborationFilePermissionCard: some View {
        VStack(alignment: .leading, spacing: 12) {
            sectionTitle("文件协作权限")

            HStack(spacing: 8) {
                TextField("文件 ID", text: $viewModel.targetFileIdInput)
                    .keyboardType(.numberPad)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 10)
                    .background(Color.white.opacity(0.35), in: RoundedRectangle(cornerRadius: 10, style: .continuous))

                Button("加载协作者") {
                    Task { await viewModel.loadFileCollaborators() }
                }
                .buttonStyle(.bordered)
            }

            HStack(spacing: 8) {
                TextField("协作者用户名", text: $viewModel.collaboratorUsernameInput)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled(true)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 10)
                    .background(Color.white.opacity(0.35), in: RoundedRectangle(cornerRadius: 10, style: .continuous))

                Picker("权限", selection: $viewModel.selectedCollaboratorPermission) {
                    ForEach(FileCollaboratorPermissionOption.allCases) { item in
                        Text(item.title).tag(item)
                    }
                }
                .pickerStyle(.menu)

                Button("授权") {
                    Task { await viewModel.grantFileCollaborator() }
                }
                .buttonStyle(.borderedProminent)
            }

            HStack(spacing: 8) {
                TextField("撤销 user_id", text: $viewModel.revokeCollaboratorUserIdInput)
                    .keyboardType(.numberPad)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 10)
                    .background(Color.white.opacity(0.35), in: RoundedRectangle(cornerRadius: 10, style: .continuous))

                Button("撤销") {
                    Task { await viewModel.revokeFileCollaborator() }
                }
                .buttonStyle(.bordered)
                .tint(.red)
            }

            if viewModel.fileCollaborators.isEmpty {
                Text("暂无文件协作者")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            } else {
                VStack(alignment: .leading, spacing: 6) {
                    ForEach(viewModel.fileCollaborators) { item in
                        Text("#\(item.userId)  \(item.username)  [\(item.permission)]")
                            .font(.system(size: 13, weight: .medium, design: .monospaced))
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(.horizontal, 10)
                            .padding(.vertical, 8)
                            .background(Color.white.opacity(0.28), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                    }
                }
            }
        }
        .padding(16)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 18, style: .continuous)
                .stroke(Color.white.opacity(0.6), lineWidth: 1)
        )
    }

    @ViewBuilder
    private var statusCard: some View {
        if let error = viewModel.errorMessage {
            statusRow(text: error, color: .red, icon: "exclamationmark.triangle.fill")
        }
        if let success = viewModel.successMessage {
            statusRow(text: success, color: .green, icon: "checkmark.circle.fill")
        }
    }

    private var logoutButton: some View {
        Button(role: .destructive) {
            Task {
                await viewModel.logout()
            }
        } label: {
            HStack {
                if viewModel.isLoggingOut {
                    ProgressView().tint(.white)
                } else {
                    Image(systemName: "rectangle.portrait.and.arrow.right")
                }
                Text(viewModel.isLoggingOut ? "退出中..." : "退出账户")
                    .font(.system(size: 16, weight: .bold, design: .rounded))
            }
            .frame(maxWidth: .infinity)
            .padding(.vertical, 14)
            .background(Color.red, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
            .foregroundStyle(.white)
        }
        .buttonStyle(.plain)
        .disabled(viewModel.isLoggingOut)
    }

    private func sectionTitle(_ text: String) -> some View {
        Text(text)
            .font(.system(size: 17, weight: .bold, design: .rounded))
    }

    private func inputField(title: String, text: Binding<String>) -> some View {
        TextField(title, text: text)
            .textInputAutocapitalization(.never)
            .autocorrectionDisabled(true)
            .padding(.horizontal, 12)
            .padding(.vertical, 10)
            .background(Color.white.opacity(0.35), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
    }

    private func secureInputField(title: String, text: Binding<String>) -> some View {
        SecureField(title, text: text)
            .textInputAutocapitalization(.never)
            .autocorrectionDisabled(true)
            .padding(.horizontal, 12)
            .padding(.vertical, 10)
            .background(Color.white.opacity(0.35), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
    }

    private func statusRow(text: String, color: Color, icon: String) -> some View {
        HStack(spacing: 8) {
            Image(systemName: icon)
            Text(text)
                .font(.system(size: 13, weight: .medium))
            Spacer()
        }
        .foregroundStyle(color)
        .padding(12)
        .background(color.opacity(0.1), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
    }
}

struct SettingsView_Previews: PreviewProvider {
    static var previews: some View {
        SettingsView()
    }
}
