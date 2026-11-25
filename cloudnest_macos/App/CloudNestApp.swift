import SwiftUI

@main
struct CloudNestApp: App {
    @State private var session: SessionState
    @State private var transfers: TransferManager
    @State private var frpCommands: FRPCommandManager

    init() {
        let session = SessionState(api: ServerAPI(), vault: KeychainSessionVault(service: "org.cloudnest.macos.session"))
        _session = State(initialValue: session)
        _transfers = State(initialValue: TransferManager(session: session))
        _frpCommands = State(initialValue: FRPCommandManager(session: session))
    }

    var body: some Scene {
        WindowGroup("CloudNest", id: "workspace") {
            WorkspaceWindow(session: session, transfers: transfers, frpCommands: frpCommands)
                .frame(minWidth: 640, minHeight: 440)
                .task {
                    await session.restore()
                    await transfers.restore()
                    await frpCommands.restore()
                }
        }
        .defaultSize(width: 900, height: 590)
        .onChange(of: session.transferIdentity) {
            frpCommands.synchronizeIdentity()
            Task { await transfers.synchronizeIdentity() }
        }
        .commands {
            FileBrowserCommands()
            CommandMenu("服务器") {
                Button("刷新连接状态") { Task { await session.refresh() } }
                    .keyboardShortcut("r", modifiers: [.command, .option])
                    .disabled(!session.hasSavedSession || session.busy)
                Button("退出登录") { Task { await session.signOut() } }
                    .disabled(!session.hasSavedSession || session.busy)
            }
        }
        Settings {
            ConnectionDetails(session: session)
                .padding()
                .frame(width: 440)
        }
    }
}

private enum Page: String, CaseIterable, Identifiable {
    case files = "我的文件"
    case recycleBin = "回收站"
    case transfers = "传输"
    case ai = "AI 检索"
    case frp = "远程连接"
    case workspaces = "协作空间"
    case shares = "分享"
    case settings = "设置"
    case connection = "服务器连接"
    case account = "账户"
    var id: Self { self }
    var icon: String {
        switch self {
        case .files: "folder"
        case .recycleBin: "trash"
        case .transfers: "arrow.up.arrow.down"
        case .ai: "sparkle.magnifyingglass"
        case .frp: "point.3.connected.trianglepath.dotted"
        case .workspaces: "person.2"
        case .shares: "link"
        case .settings: "gearshape"
        case .connection: "network"
        case .account: "person.crop.circle"
        }
    }
}

struct WorkspaceWindow: View {
    let session: SessionState
    let transfers: TransferManager
    let frpCommands: FRPCommandManager
    @State private var selection: Page? = .files

    var body: some View {
        if session.hasSavedSession {
            NavigationSplitView {
                List(selection: $selection) {
                    ForEach(Page.allCases) { page in
                        Label(page.rawValue, systemImage: page.icon)
                            .badge(page == .transfers ? transfers.jobs.filter {
                                !transfers.legacyPreviewJobs.contains($0.id) && ($0.phase.isActive || $0.phase == .queued)
                            }.count : 0)
                            .tag(page)
                    }
                }
                .navigationSplitViewColumnWidth(min: 170, ideal: 200, max: 260)
            } detail: {
                Group {
                    switch selection ?? .connection {
                    case .files:
                        FileBrowserView(session: session, transfers: transfers, recycleBin: false)
                            .id(session.account?.id)
                    case .recycleBin:
                        FileBrowserView(session: session, transfers: transfers, recycleBin: true)
                            .id(session.account?.id)
                    case .transfers:
                        TransferListView(transfers: transfers)
                    case .ai:
                        AISearchView(session: session, transfers: transfers)
                            .id(session.transferIdentity)
                    case .frp:
                        FRPView(session: session, commands: frpCommands)
                            .id(session.transferIdentity)
                    case .workspaces:
                        WorkspacesView(session: session, transfers: transfers)
                            .id(session.transferIdentity)
                    case .shares:
                        SharesView(session: session).id(session.transferIdentity)
                    case .settings:
                        SettingsView(session: session).id(session.transferIdentity)
                    case .connection:
                        ConnectionDetails(session: session)
                    case .account:
                        Form {
                            LabeledContent("用户名", value: session.account?.username ?? "—")
                            LabeledContent("邮箱", value: session.account?.email ?? "—")
                            if let account = session.account {
                                LabeledContent("角色", value: account.isAdmin ? "管理员" : "用户")
                            }
                            Button("退出登录") { Task { await session.signOut() } }
                                .disabled(session.busy)
                            if let error = session.error {
                                Text(error).foregroundStyle(.secondary)
                            }
                        }
                        .formStyle(.grouped)
                    }
                }
                .navigationTitle((selection ?? .connection).rawValue)
                .toolbar {
                    if selection == .connection || selection == .account {
                        ToolbarItem {
                            Button {
                                Task { await session.refresh() }
                            } label: {
                                Label("刷新连接状态", systemImage: "arrow.clockwise")
                            }
                            .disabled(session.busy)
                        }
                    }
                }
            }
        } else if session.restored {
            SignInView(session: session)
        } else if let error = session.error {
            ContentUnavailableView {
                Label("无法读取会话", systemImage: "key")
            } description: {
                Text(error)
            } actions: {
                Button("重试") { Task { await session.restore() } }
                    .disabled(session.busy)
            }
        } else {
            ProgressView("正在读取会话")
        }
    }
}

private struct SignInView: View {
    let session: SessionState
    @AppStorage("lastServerOrigin") private var lastServer = ""
    @State private var server = ""
    @State private var username = ""
    @State private var password = ""
    @State private var showingShare = false
    @ScaledMetric(relativeTo: .body) private var formHeight: CGFloat = 168

    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            Label("CloudNest", systemImage: "externaldrive.connected.to.line.below")
                .font(.largeTitle.weight(.semibold))
            Form {
                TextField("服务器", text: $server, prompt: Text("https://cloud.example.com"))
                    .textContentType(.URL)
                TextField("用户名", text: $username)
                    .textContentType(.username)
                SecureField("密码", text: $password)
                    .textContentType(.password)
            }
            .formStyle(.grouped)
            .frame(height: formHeight)
            if let error = session.error {
                Label(error, systemImage: "exclamationmark.circle")
                    .foregroundStyle(.secondary)
            }
            HStack {
                Button("打开分享链接…") { showingShare = true }.disabled(session.busy)
                Spacer()
                if session.busy { ProgressView().controlSize(.small) }
                Button("登录", action: signIn)
                    .buttonStyle(.borderedProminent)
                    .keyboardShortcut(.defaultAction)
                    .disabled(session.busy || server.isEmpty || username.isEmpty || password.isEmpty)
            }
        }
        .frame(maxWidth: 410)
        .padding(28)
        .onAppear { server = session.origin?.url.absoluteString ?? lastServer }
        .sheet(isPresented: $showingShare) { PublicShareView(client: session.publicShareClient) }
    }

    private func signIn() {
        let enteredPassword = password
        password = ""
        Task {
            await session.signIn(server: server, username: username, password: enteredPassword)
            if session.hasSavedSession, let origin = session.origin { lastServer = origin.url.absoluteString }
        }
    }
}

private struct ConnectionDetails: View {
    let session: SessionState

    var body: some View {
        Form {
            Section("服务器") {
                LabeledContent("地址", value: session.origin?.url.absoluteString ?? "—")
                LabeledContent("状态") {
                    switch session.connection {
                    case .unchecked: Label("未登录", systemImage: "circle")
                    case .checking: ProgressView().controlSize(.small).accessibilityLabel("正在检查")
                    case .connected: Label("已连接", systemImage: "checkmark.circle")
                    case .restricted: Label("已登录，健康检查受限", systemImage: "lock")
                    case .unavailable: Label("暂不可用", systemImage: "exclamationmark.circle")
                    }
                }
                if let error = session.error {
                    Text(error).foregroundStyle(.secondary)
                }
            }
        }
        .formStyle(.grouped)
    }
}
