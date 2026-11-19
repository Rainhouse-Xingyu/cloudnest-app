import SwiftUI

@main
struct CloudNestApp: App {
    @State private var session = SessionState(
        api: ServerAPI(),
        vault: KeychainSessionVault(service: "org.cloudnest.macos.session")
    )

    var body: some Scene {
        WindowGroup("CloudNest", id: "workspace") {
            WorkspaceWindow(session: session)
                .frame(minWidth: 640, minHeight: 440)
                .task { await session.restore() }
        }
        .defaultSize(width: 900, height: 590)
        .commands {
            CommandMenu("服务器") {
                Button("刷新连接状态") { Task { await session.refresh() } }
                    .keyboardShortcut("r")
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
    case connection = "服务器连接"
    case account = "账户"
    var id: Self { self }
    var icon: String { self == .connection ? "network" : "person.crop.circle" }
}

struct WorkspaceWindow: View {
    let session: SessionState
    @State private var selection: Page? = .connection

    var body: some View {
        if session.hasSavedSession {
            NavigationSplitView {
                List(selection: $selection) {
                    ForEach(Page.allCases) { page in
                        Label(page.rawValue, systemImage: page.icon).tag(page)
                    }
                }
                .navigationSplitViewColumnWidth(min: 170, ideal: 200, max: 260)
            } detail: {
                Group {
                    switch selection ?? .connection {
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
            if let error = session.error {
                Label(error, systemImage: "exclamationmark.circle")
                    .foregroundStyle(.secondary)
            }
            HStack {
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
