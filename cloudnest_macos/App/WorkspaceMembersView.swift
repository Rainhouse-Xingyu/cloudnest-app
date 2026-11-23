import SwiftUI

struct WorkspaceMembersView: View {
    let session: SessionState
    @Environment(\.dismiss) private var dismiss
    @State private var state: WorkspaceMembersState
    @State private var editor: Editor?
    @State private var removing: WorkspaceMember?
    @State private var attemptedRemovals: Set<Int64> = []
    @State private var writing = false
    @State private var error: String?
    private let identity: TransferIdentity?
    private struct Editor: Identifiable { let id = UUID(); let original: WorkspaceMember? }

    init(session: SessionState, workspace: Workspace) {
        self.session = session; identity = session.transferIdentity
        _state = State(initialValue: WorkspaceMembersState(session: session, workspace: workspace))
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("空间成员").font(.title2)
            Text("\(state.workspace.name) · 上次读取角色：\(state.workspace.role.title)").foregroundStyle(.secondary)
            HStack {
                Button("刷新") { Task { await state.load() } }.disabled(state.busy || writing)
                if state.workspace.role.canManageMembers {
                    Button("添加成员…") { editor = Editor(original: nil) }.disabled(state.busy || writing || state.error != nil)
                }
                Spacer()
                if state.busy || writing { ProgressView().controlSize(.small) }
            }
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 12) {
                    ForEach(state.members) { member in
                        VStack(alignment: .leading, spacing: 6) {
                            Text(member.username).font(.headline)
                            Text("用户 \(String(member.userID)) · \(member.role.title)").foregroundStyle(.secondary)
                            if state.workspace.canManage(member) {
                                HStack {
                                    Button("更改角色…") { editor = Editor(original: member) }.disabled(writing || state.busy)
                                    Button("移除成员…", role: .destructive) { removing = member }
                                        .disabled(writing || state.busy || attemptedRemovals.contains(member.id))
                                }
                            }
                        }
                        Divider()
                    }
                    if let error = state.error { Text(error).foregroundStyle(.secondary) }
                    if let error { Text(error).foregroundStyle(.secondary) }
                    Text("所有者身份不能在成员接口转让。管理员只能管理非管理员成员；角色更改按已读内容核对，实际权限由服务器在提交时重新验证。")
                        .font(.callout).foregroundStyle(.secondary)
                }.frame(maxWidth: .infinity, alignment: .leading)
            }.frame(minHeight: 80, maxHeight: .infinity).layoutPriority(-1)
            HStack { Spacer(); Button("关闭") { dismiss() }.keyboardShortcut(.cancelAction).disabled(writing) }
                .fixedSize(horizontal: false, vertical: true)
        }.padding(24).frame(width: 520).frame(minHeight: 360, idealHeight: 520)
        .task { await state.load() }
        .onDisappear { state.cancel() }
        .sheet(item: $editor, onDismiss: { Task { await state.load() } }) { item in
            WorkspaceMemberEditor(session: session, workspace: state.workspace, original: item.original)
        }
        .confirmationDialog("移除空间成员", isPresented: Binding(get: { removing != nil }, set: { if !$0 { removing = nil } }), presenting: removing) { member in
            Button("确认移除", role: .destructive) { Task { await remove(member) } }
        } message: { member in
            Text("从「\(state.workspace.name)」移除「\(member.username)」（\(member.role.title)）。影响后续空间访问；已开始的下载可能按读取租约完成。")
        }
    }

    private func remove(_ member: WorkspaceMember) async {
        guard !writing, state.workspace.canManage(member), attemptedRemovals.insert(member.id).inserted else { return }
        writing = true; error = nil
        defer { writing = false }
        do {
            try await session.changeWorkspaceMember(workspaceID: state.workspace.id, original: member, role: nil, expectedIdentity: identity)
            try session.requireTransferIdentity(identity)
        } catch {
            if let rejection = error as? ServerError, [.conflict, .forbidden, .notFound, .invalidOperation, .rateLimited].contains(rejection) {
                attemptedRemovals.remove(member.id)
            }
            self.error = error as? ServerError == .conflict
                ? "成员角色已变化，请刷新核对后重新确认。"
                : "未取得移除成功回执，请关闭后重新打开成员列表核对。\((error as? ServerError)?.localizedDescription ?? "连接未能完成。")"
        }
        await state.load()
    }
}

private struct WorkspaceMemberEditor: View {
    let session: SessionState
    let workspace: Workspace
    let original: WorkspaceMember?
    private let identity: TransferIdentity?
    @Environment(\.dismiss) private var dismiss
    @State private var username: String
    @State private var role: WorkspaceRole
    @State private var busy = false
    @State private var submitted = false
    @State private var error: String?
    @State private var confirming = false
    private var roles: [WorkspaceRole] { [.viewer, .editor, .member] + (workspace.role == .owner ? [.admin] : []) }

    init(session: SessionState, workspace: Workspace, original: WorkspaceMember?) {
        self.session = session; self.workspace = workspace; self.original = original; identity = session.transferIdentity
        _username = State(initialValue: original?.username ?? ""); _role = State(initialValue: original?.role ?? .viewer)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text(original == nil ? "添加空间成员" : "更改成员角色").font(.title2)
            Text(workspace.name).foregroundStyle(.secondary)
            TextField("现有账户的用户名", text: $username).disabled(original != nil || busy || submitted)
            Picker("角色", selection: $role) { ForEach(roles, id: \.self) { Text($0.title).tag($0) } }.disabled(busy || submitted)
            Text(role.canWrite ? "该角色可以新增、编辑和删除空间文件。空间管理员还可管理非管理员成员。" : "该角色只能浏览和下载空间文件，不能写入。")
                .font(.callout).foregroundStyle(.secondary)
            if let error { Text(error).foregroundStyle(.secondary) }
            HStack {
                if busy { ProgressView().controlSize(.small) }
                Spacer()
                Button("关闭") { dismiss() }.keyboardShortcut(.cancelAction).disabled(busy)
                Button("保存…") { confirming = true }.disabled(busy || submitted || username.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
        }.padding(24).frame(width: 500)
        .interactiveDismissDisabled(busy)
        .confirmationDialog("确认空间访问权限", isPresented: $confirming, titleVisibility: .visible) {
            Button("确认保存") { Task { await save() } }
        } message: {
            Text("空间：\(workspace.name)。账户：\(username)。角色：\(role.title)。\(original == nil ? "仅新增成员，已有成员需刷新后另行编辑。" : "核对已读角色，变化时拒绝覆盖。")")
        }
    }

    private func save() async {
        guard !busy, !submitted, roles.contains(role), workspace.role.canManageMembers,
              original.map({ workspace.canManage($0) }) ?? true else { return }
        busy = true; submitted = true; error = nil
        defer { busy = false }
        do {
            if let original {
                try await session.changeWorkspaceMember(workspaceID: workspace.id, original: original, role: role, expectedIdentity: identity)
            } else {
                try await session.addWorkspaceMember(workspaceID: workspace.id, username: username, role: role, expectedIdentity: identity)
            }
            try session.requireTransferIdentity(identity)
            dismiss()
        } catch {
            if error as? ServerError == .conflict {
                self.error = "成员已存在或角色已变化，请关闭并刷新后核对。"
            } else if let rejection = error as? ServerError, [.invalidOperation, .forbidden, .notFound, .rateLimited].contains(rejection) {
                submitted = false; self.error = rejection.localizedDescription
            } else { self.error = "未取得保存成功回执，请关闭并刷新成员核对，避免重复请求。" }
        }
    }
}
