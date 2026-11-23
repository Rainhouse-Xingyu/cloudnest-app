import SwiftUI

struct FileDestinationAction: Identifiable {
    let id = UUID()
    let ids: [Int64]
    let copy: Bool
}

struct FileDestinationSheet: View {
    let action: FileDestinationAction
    let submit: (Int64) -> Void
    let session: SessionState
    let sourceRoot: FileBrowserRoot
    private let identity: TransferIdentity?
    @State private var browser: FileBrowserState
    @State private var roots: [FileBrowserRoot]
    @State private var selectedRootID: Int64
    @State private var rootsError: String?
    @Environment(\.dismiss) private var dismiss

    init(session: SessionState, action: FileDestinationAction, root: FileBrowserRoot = .personal, submit: @escaping (Int64) -> Void) {
        self.action = action
        self.submit = submit
        self.session = session; sourceRoot = root; identity = session.transferIdentity
        _browser = State(initialValue: FileBrowserState(session: session, recycleBin: false, root: root))
        _roots = State(initialValue: [root]); _selectedRootID = State(initialValue: root.id)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text(action.copy ? "复制到" : "移动到").font(.headline)
            if roots.count > 1 {
                Picker("目标范围", selection: $selectedRootID) {
                    ForEach(roots, id: \.id) { root in Text(root.title).tag(root.id) }
                }
            }
            HStack {
                Button(browser.root.title) { Task { await browser.navigate(to: 0) } }
                ForEach(Array(browser.folders.enumerated()), id: \.element.id) { index, item in
                    Image(systemName: "chevron.right").foregroundStyle(.secondary)
                    Button(item.filename) { Task { await browser.navigate(to: index + 1) } }
                }
            }
            .buttonStyle(.borderless)
            List(browser.items.filter(\.isDirectory)) { item in
                Button {
                    Task { await browser.open(item) }
                } label: {
                    Label(item.filename, systemImage: "folder")
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .buttonStyle(.plain)
                .disabled(action.ids.contains(item.id))
            }
            .overlay {
                if browser.busy { ProgressView() }
            }
            if let error = browser.error {
                HStack {
                    Text(error).foregroundStyle(.secondary)
                    Button("重试") { Task { await browser.reload() } }
                }
            }
            Text("同名项目将自动分配新名称。")
                .font(.caption)
                .foregroundStyle(.secondary)
            if sourceRoot.workspaceID != nil {
                Text("仅选择同一空间目录，不能移出或复制出空间。")
                    .font(.caption).foregroundStyle(.secondary)
            }
            if let rootsError { Text(rootsError).font(.caption).foregroundStyle(.secondary) }
            HStack {
                Text(browser.title).foregroundStyle(.secondary)
                Spacer()
                Button("取消", role: .cancel) { dismiss() }
                    .keyboardShortcut(.cancelAction)
                Button(action.copy ? "复制" : "移动") {
                    submit(browser.parentID)
                    dismiss()
                }
                .keyboardShortcut(.defaultAction)
                .disabled(!browser.root.canWrite || browser.busy || !browser.loaded || browser.error != nil || action.ids.contains(browser.parentID))
            }
        }
        .padding(22)
        .frame(width: 520, height: 400)
        .task {
            await browser.reload()
            guard sourceRoot.workspaceID == nil, action.copy else { return }
            do {
                try session.requireTransferIdentity(identity)
                let spaces = try await session.workspaces()
                try Task.checkCancellation()
                try session.requireTransferIdentity(identity)
                roots = [.personal] + spaces.filter { $0.role.canWrite }.compactMap(FileBrowserRoot.init(workspace:))
            } catch is CancellationError { return }
            catch { rootsError = "未能读取可写空间，仍可选择个人目录。" }
        }
        .onChange(of: selectedRootID) { _, id in
            guard let root = roots.first(where: { $0.id == id }) else { return }
            let next = FileBrowserState(session: session, recycleBin: false, root: root)
            browser = next
            Task { await next.reload() }
        }
    }
}
