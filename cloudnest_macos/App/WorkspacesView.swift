import SwiftUI

struct WorkspacesView: View {
    let session: SessionState
    let transfers: TransferManager
    @State private var state: WorkspaceBrowserState
    @State private var selectedID: Int64?
    @State private var creating = false
    @State private var members: Workspace?

    init(session: SessionState, transfers: TransferManager) {
        self.session = session; self.transfers = transfers
        _state = State(initialValue: WorkspaceBrowserState(session: session))
    }

    private var selected: Workspace? { state.items.first { $0.id == selectedID } }

    var body: some View {
        Group {
            if let workspace = selected, let root = FileBrowserRoot(workspace: workspace) {
                FileBrowserView(session: session, transfers: transfers, recycleBin: false, root: root)
                    .id(root)
                    .navigationSubtitle("\(workspace.role.canWrite ? "" : "只读空间 · ")上次读取角色：\(workspace.role.title)")
                    .toolbar {
                        ToolbarItem(placement: .navigation) {
                            Button("所有协作空间", systemImage: "chevron.left") { selectedID = nil }
                                .help("返回协作空间列表")
                        }
                        ToolbarItem(placement: .primaryAction) {
                            Menu {
                                Text("上次读取角色：\(workspace.role.title)")
                                Button("刷新空间权限") { Task { await state.load() } }.disabled(state.busy)
                                Button("成员…") { members = workspace }
                            } label: { Label("空间成员与权限", systemImage: "person.2") }
                        }
                    }
            } else {
                VStack(spacing: 0) {
                    ScrollView {
                        LazyVStack(alignment: .leading, spacing: 0) {
                            ForEach(state.items) { workspace in
                                HStack {
                                    VStack(alignment: .leading, spacing: 5) {
                                        Text(workspace.name).font(.headline)
                                        Text("空间 \(String(workspace.id)) · \(workspace.role.title)").foregroundStyle(.secondary)
                                        if workspace.rootFileID == nil {
                                            Text("旧空间尚无托管根目录，可查看成员，文件归属迁移需另行处理。")
                                                .font(.callout).foregroundStyle(.secondary)
                                        }
                                    }.fixedSize(horizontal: false, vertical: true)
                                    Spacer()
                                    Button("成员…") { members = workspace }
                                        .accessibilityLabel("查看 \(workspace.name) 的成员")
                                    Button("打开文件") { selectedID = workspace.id }
                                        .accessibilityLabel("打开 \(workspace.name) 的文件")
                                        .disabled(workspace.rootFileID == nil)
                                }
                                .padding(16)
                                Divider().padding(.horizontal, 16)
                            }
                        }.frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .overlay {
                        if state.busy && !state.loaded { ProgressView("正在读取协作空间") }
                        else if state.loaded && state.items.isEmpty && state.error == nil {
                            ContentUnavailableView("没有可访问的协作空间", systemImage: "person.2", description: Text("可以创建空间，或请空间管理员添加你的账户。"))
                        }
                    }
                    Text("空间角色决定文件权限。成员变化影响后续请求；已开始的下载按读取租约处理。")
                        .font(.callout).foregroundStyle(.secondary).padding(12)
                }
            }
        }
        .navigationTitle(selected?.name ?? "协作空间")
        .safeAreaInset(edge: .bottom) {
            if let error = state.error {
                HStack {
                    Label(error, systemImage: "exclamationmark.circle").foregroundStyle(.secondary)
                    Spacer()
                    Button("重试") { Task { await state.load() } }.disabled(state.busy)
                }.padding(12)
            }
        }
        .toolbar {
            if selected == nil {
                Button("创建空间", systemImage: "plus") { creating = true }.disabled(state.busy)
                Button("刷新空间", systemImage: "arrow.clockwise") { Task { await state.load() } }.disabled(state.busy)
            }
        }
        .sheet(isPresented: $creating, onDismiss: { Task { await state.load() } }) {
            WorkspaceCreateView(session: session)
        }
        .sheet(item: $members, onDismiss: { Task { await state.load() } }) { workspace in
            WorkspaceMembersView(session: session, workspace: workspace)
        }
        .task { await state.load() }
        .onDisappear { state.cancel() }
        .onChange(of: state.items) { _, rows in
            if let selectedID, !rows.contains(where: { $0.id == selectedID }) { self.selectedID = nil }
        }
    }
}

private struct WorkspaceCreateView: View {
    let session: SessionState
    private let identity: TransferIdentity?
    @Environment(\.dismiss) private var dismiss
    @State private var name = ""
    @State private var busy = false
    @State private var submitted = false
    @State private var error: String?
    @State private var confirming = false

    init(session: SessionState) { self.session = session; identity = session.transferIdentity }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("创建协作空间").font(.title2)
            TextField("空间名称", text: $name).disabled(busy || submitted)
            Text("创建专属根目录，你将成为空间所有者。成员需另行确认添加，不自动转移个人文件。")
                .font(.callout).foregroundStyle(.secondary)
            if let error { Text(error).foregroundStyle(.secondary) }
            HStack {
                if busy { ProgressView().controlSize(.small) }
                Spacer()
                Button("关闭") { dismiss() }.keyboardShortcut(.cancelAction).disabled(busy)
                Button("创建…") { confirming = true }.disabled(busy || submitted || name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
        }.padding(24).frame(width: 480)
        .interactiveDismissDisabled(busy)
        .confirmationDialog("创建协作空间", isPresented: $confirming, titleVisibility: .visible) {
            Button("确认创建") { Task { await save() } }
        } message: { Text("创建「\(name)」及其根目录，不邀请其他成员。") }
    }

    private func save() async {
        guard !busy, !submitted else { return }
        busy = true; submitted = true; error = nil
        defer { busy = false }
        do {
            _ = try await session.createWorkspace(name: name, expectedIdentity: identity)
            try session.requireTransferIdentity(identity)
            dismiss()
        } catch {
            if let rejection = error as? ServerError, [.invalidOperation, .forbidden, .rateLimited].contains(rejection) {
                submitted = false
                self.error = rejection == .invalidOperation ? "名称须为 1–100 个 Unicode 字符，不能包含控制字符。" : rejection.localizedDescription
            } else { self.error = "未取得创建成功回执，请关闭并刷新空间列表核对，避免重复创建。" }
        }
    }
}
