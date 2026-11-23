import SwiftUI
import AppKit
import QuickLook

struct FileBrowserView: View {
    let transfers: TransferManager
    let session: SessionState
    @State private var browser: FileBrowserState
    @State private var showingInspector = false
    @State private var naming: NameAction?
    @State private var confirmingTrash = false
    @State private var destination: FileDestinationAction?
    @State private var versionFile: FileItem?
    @State private var collaboratorsFile: FileItem?
    @State private var previewRequest: UUID?
    @State private var previewURL: URL?
    @State private var presentedPreview: UUID?
    @State private var previewGeneration = UUID()

    init(session: SessionState, transfers: TransferManager, recycleBin: Bool, root: FileBrowserRoot = .personal) {
        self.transfers = transfers
        self.session = session
        _browser = State(initialValue: FileBrowserState(session: session, recycleBin: recycleBin, root: root))
    }

    var body: some View {
        VStack(spacing: 0) {
            if !browser.recycleBin && !browser.folders.isEmpty {
                breadcrumbs
                Divider()
            }
            Table(browser.visibleItems, selection: $browser.selection, sortOrder: $browser.sortOrder) {
                TableColumn("名称", value: \.filename) { item in
                    Label(item.filename, systemImage: item.isDirectory ? "folder" : "doc")
                }
                .width(min: 160, ideal: 300)
                TableColumn("大小", value: \.size) { item in
                    Text(item.isDirectory ? "—" : item.size.formatted(.byteCount(style: .file)))
                }
                .width(min: 75, ideal: 95, max: 130)
                TableColumn("修改时间", value: \.updateTime) { item in
                    if let modified = item.modified {
                        Text(modified, format: .dateTime.year().month().day().hour().minute())
                    }
                }
                .width(min: 130, ideal: 160, max: 200)
            }
            .contextMenu(forSelectionType: Int64.self) { ids in
                selectionMenu(ids)
            } primaryAction: { ids in
                if ids.count == 1, let item = browser.items.first(where: { ids.contains($0.id) }) {
                    if item.isDirectory { Task { await browser.open(item) } }
                    else { preparePreview(item) }
                }
            }
            .dropDestination(for: URL.self) { urls, _ in
                guard !browser.recycleBin, browser.root.canWrite, !urls.isEmpty, urls.allSatisfy(\.isFileURL),
                      let identity = session.transferIdentity else { return false }
                let parentID = browser.parentID
                Task { await queueUploads(urls, parentID: parentID, identity: identity) }
                return true
            }
            .onDeleteCommand {
                if !browser.recycleBin && browser.root.canWrite && !browser.selectedItems.isEmpty { confirmingTrash = true }
            }
            .focusedValue(\.fileTableActions, fileActions)
            .onKeyPress(keys: [.space, .return]) { press in
                guard !browser.recycleBin, !browser.busy else { return .ignored }
                guard press.modifiers.isEmpty, browser.selectedItems.count == 1,
                      let item = browser.selectedItems.first else { return .ignored }
                if press.key == .space, !item.isDirectory {
                    preparePreview(item)
                    return .handled
                }
                if press.key == .return, browser.root.canWrite {
                    naming = .rename(item)
                    return .handled
                }
                return .ignored
            }
            .overlay {
                if browser.busy && !browser.loaded { ProgressView("正在读取文件") }
                if browser.loaded && browser.items.isEmpty && browser.error == nil {
                    ContentUnavailableView(browser.recycleBin ? "回收站为空" : "此文件夹为空",
                                           systemImage: browser.recycleBin ? "trash" : "folder")
                }
            }
            if let id = previewRequest, let job = transfers.records[id], job.phase != .completed {
                HStack {
                    if job.phase.isActive { ProgressView().controlSize(.small) }
                    Text(job.error ?? "正在下载并验证预览 · \(job.confirmedBytes.formatted(.byteCount(style: .file))) / \(job.totalBytes.formatted(.byteCount(style: .file)))")
                    Spacer()
                    Button("取消预览") {
                        clearPreview()
                    }
                }
                .foregroundStyle(.secondary)
                .padding(12)
            }
            if let error = transfers.error {
                Label(error, systemImage: "exclamationmark.circle")
                    .foregroundStyle(.secondary)
                    .padding(12)
            }
            if let error = browser.error {
                HStack {
                    Label(error, systemImage: "exclamationmark.circle")
                        .foregroundStyle(.secondary)
                    Spacer()
                    Button("重试") { Task { await browser.reload() } }
                        .disabled(browser.busy)
                }
                .padding(12)
            }
        }
        .navigationTitle(browser.title)
        .searchable(text: $browser.filter, prompt: "筛选当前文件夹")
        .onChange(of: browser.filter) {
            browser.selection.formIntersection(Set(browser.visibleItems.map(\.id)))
        }
        .toolbar {
            ToolbarItemGroup {
                if !browser.recycleBin {
                    Button("上传", systemImage: "arrow.up.doc", action: chooseUploads)
                        .disabled(!browser.root.canWrite)
                    Button("下载", systemImage: "arrow.down.doc") { chooseDownloads(browser.selectedItems) }
                        .disabled(browser.selectedItems.isEmpty)
                    Button {
                        naming = .create
                    } label: {
                        Label("新建文件夹", systemImage: "folder.badge.plus")
                    }
                    .disabled(!browser.root.canWrite)
                    Button("协作者", systemImage: "person.2") { collaboratorsFile = browser.selectedItems.first }
                        .disabled(browser.selectedItems.count != 1 || browser.selectedItems.first?.workspaceID != nil)
                } else {
                    Button("恢复", systemImage: "arrow.uturn.backward") {
                        Task { await browser.restoreSelected() }
                    }
                    .disabled(browser.selectedItems.count != 1)
                }
                Button {
                    Task { await browser.reload() }
                } label: {
                    Label("刷新文件", systemImage: "arrow.clockwise")
                }
                Button {
                    showingInspector.toggle()
                } label: {
                    Label("检查器", systemImage: "sidebar.right")
                }
            }
        }
        .disabled(browser.busy)
        .focusedSceneValue(\.fileBrowserActions, fileActions)
        .inspector(isPresented: $showingInspector) {
            FileInspector(items: browser.selectedItems) { versionFile = $0 }
                .inspectorColumnWidth(min: 220, ideal: 260, max: 350)
        }
        .quickLookPreview($previewURL)
        .sheet(item: $destination) { action in
            FileDestinationSheet(session: session, action: action, root: browser.root) { target in
                Task { await browser.relocate(ids: action.ids, parentID: target, copy: action.copy) }
            }
        }
        .sheet(item: $versionFile) { item in
            FileVersionsSheet(file: item, session: session, transfers: transfers, canWrite: browser.root.canWrite) {
                Task { await browser.reload() }
            }
        }
        .sheet(item: $collaboratorsFile) { item in
            FileCollaboratorsView(session: session, file: item)
        }
        .sheet(item: $naming) { action in
            FileNameSheet(action: action) { name in
                Task {
                    switch action {
                    case .create: await browser.createFolder(name: name)
                    case .rename(let item): await browser.rename(item, name: name)
                    }
                }
            }
        }
        .confirmationDialog("将选中的 \(browser.selectedItems.count) 个项目移入回收站？", isPresented: $confirmingTrash) {
            Button("移入回收站", role: .destructive) { Task { await browser.trashSelected() } }
        }
        .task { await browser.reload() }
        .onChange(of: previewRequest.flatMap { transfers.records[$0]?.phase }) {
            guard let id = previewRequest, transfers.records[id]?.phase == .completed else { return }
            let generation = previewGeneration
            Task {
                do {
                    let url = try await transfers.resultURL(id)
                    guard generation == previewGeneration, previewRequest == id else { return }
                    presentedPreview = id
                    previewURL = url
                } catch { transfers.report(error) }
            }
        }
        .onChange(of: previewURL) { _, url in
            if url == nil, let id = presentedPreview {
                presentedPreview = nil
                if previewRequest == id { previewRequest = nil }
                Task { await transfers.dismissPreview(id) }
            }
        }
        .onDisappear { clearPreview() }
        .onChange(of: transfers.jobs.filter { $0.direction.isUpload && $0.phase == .completed }.count) {
            Task { await browser.reload() }
        }
    }

    private func preparePreview(_ item: FileItem) {
        guard let identity = session.transferIdentity else { return }
        let generation = UUID()
        previewGeneration = generation
        let previous = previewRequest
        previewRequest = nil
        presentedPreview = nil
        previewURL = nil
        Task {
            if let previous { await transfers.dismissPreview(previous) }
            do {
                let id = try await transfers.enqueuePreview(item, expectedIdentity: identity)
                if generation == previewGeneration { previewRequest = id }
                else { await transfers.dismissPreview(id) }
            } catch { transfers.report(error) }
        }
    }

    private func clearPreview() {
        previewGeneration = UUID()
        let previous = previewRequest
        previewRequest = nil
        presentedPreview = nil
        previewURL = nil
        if let previous { Task { await transfers.dismissPreview(previous) } }
    }

    private var fileActions: FileBrowserActions {
        var actions = FileBrowserActions()
        guard !browser.busy, naming == nil, destination == nil, versionFile == nil, collaboratorsFile == nil, !confirmingTrash else { return actions }
        actions.refresh = { Task { await browser.reload() } }
        actions.inspector = { showingInspector.toggle() }
        let selected = browser.selectedItems
        if browser.recycleBin {
            if selected.count == 1 { actions.restore = { Task { await browser.restoreSelected() } } }
            return actions
        }
        if browser.root.canWrite {
            actions.createFolder = { naming = .create }
            actions.upload = chooseUploads
        }
        if !selected.isEmpty {
            actions.download = { chooseDownloads(browser.selectedItems) }
            if browser.root.canWrite {
                actions.move = { destination = FileDestinationAction(ids: browser.selection.sorted(), copy: false) }
                actions.copy = { destination = FileDestinationAction(ids: browser.selection.sorted(), copy: true) }
                actions.trash = { confirmingTrash = true }
            }
        }
        if selected.count == 1, let item = selected.first {
            if browser.root.canWrite { actions.rename = { naming = .rename(item) } }
            if !item.isDirectory {
                actions.preview = { preparePreview(item) }
                actions.versions = { versionFile = item }
            }
        }
        return actions
    }

    private func chooseUploads() {
        guard browser.root.canWrite else { return }
        guard let identity = session.transferIdentity else { return }
        let panel = NSOpenPanel()
        panel.canChooseFiles = true
        panel.canChooseDirectories = false
        panel.allowsMultipleSelection = true
        let parentID = browser.parentID
        panel.begin { response in
            guard response == .OK else { return }
            let urls = panel.urls
            Task { await queueUploads(urls, parentID: parentID, identity: identity) }
        }
    }

    private func queueUploads(_ urls: [URL], parentID: Int64, identity: TransferIdentity) async {
        for url in urls {
            do { try await transfers.enqueueUpload(url, parentID: parentID, expectedIdentity: identity) }
            catch {
                transfers.report(error)
                if session.transferIdentity != identity { return }
            }
        }
    }

    private func chooseDownloads(_ items: [FileItem]) {
        guard !items.isEmpty, let identity = session.transferIdentity else { return }
        if items.count == 1, let item = items.first {
            let panel = NSSavePanel()
            panel.nameFieldStringValue = item.isDirectory ? "\(item.filename).zip" : item.filename
            panel.canCreateDirectories = true
            panel.begin { response in
                guard response == .OK, let url = panel.url else { return }
                Task {
                    do { try await transfers.enqueueDownload(item, destination: url, expectedIdentity: identity) }
                    catch { transfers.report(error) }
                }
            }
        } else {
            let panel = NSOpenPanel()
            panel.canChooseDirectories = true
            panel.canChooseFiles = false
            panel.canCreateDirectories = true
            panel.begin { response in
                guard response == .OK, let directory = panel.url else { return }
                Task {
                    for item in items {
                        guard item.filename != ".", item.filename != "..",
                              URL(fileURLWithPath: item.filename).lastPathComponent == item.filename else {
                            transfers.report(ServerError.invalidResponse)
                            continue
                        }
                        let name = item.isDirectory ? "\(item.filename).zip" : item.filename
                        do { try await transfers.enqueueDownload(item, destination: directory.appending(path: name), expectedIdentity: identity) }
                        catch {
                            transfers.report(error)
                            if session.transferIdentity != identity { return }
                        }
                    }
                }
            }
        }
    }

    private var breadcrumbs: some View {
        ScrollView(.horizontal) {
            HStack(spacing: 6) {
                Button(browser.root.title) { Task { await browser.navigate(to: 0) } }
                ForEach(Array(browser.folders.enumerated()), id: \.element.id) { index, folder in
                    Image(systemName: "chevron.right").foregroundStyle(.secondary)
                    Button(folder.filename) { Task { await browser.navigate(to: index + 1) } }
                }
            }
            .buttonStyle(.borderless)
            .padding(10)
        }
    }

    @ViewBuilder private func selectionMenu(_ ids: Set<Int64>) -> some View {
        if !ids.isEmpty {
            if browser.recycleBin {
                Button("恢复") {
                    browser.selection = ids
                    Task { await browser.restoreSelected() }
                }
                .disabled(ids.count != 1)
            } else {
                Button("下载…") { chooseDownloads(browser.items.filter { ids.contains($0.id) }) }
                if browser.root.canWrite {
                    Button("移动到…") { destination = FileDestinationAction(ids: ids.sorted(), copy: false) }
                    Button("复制到…") { destination = FileDestinationAction(ids: ids.sorted(), copy: true) }
                }
                if ids.count == 1, let item = browser.items.first(where: { ids.contains($0.id) }) {
                    if item.isDirectory {
                        Button("打开") { Task { await browser.open(item) } }
                    }
                    if !item.isDirectory { Button("快速查看") { preparePreview(item) } }
                    if !item.isDirectory { Button("版本历史…") { versionFile = item } }
                    if item.workspaceID == nil { Button("协作者…") { collaboratorsFile = item } }
                    if browser.root.canWrite { Button("重新命名") { naming = .rename(item) } }
                }
                if browser.root.canWrite {
                    Button("移入回收站", role: .destructive) {
                        browser.selection = ids
                        confirmingTrash = true
                    }
                }
            }
        }
    }
}

private enum NameAction: Identifiable {
    case create
    case rename(FileItem)
    var id: String {
        switch self {
        case .create: "create"
        case .rename(let item): "rename-\(item.id)"
        }
    }
    var title: String {
        switch self {
        case .create: "新建文件夹"
        case .rename: "重新命名"
        }
    }
    var name: String {
        switch self {
        case .create: ""
        case .rename(let item): item.filename
        }
    }
}

private struct FileNameSheet: View {
    let action: NameAction
    let submit: (String) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var name: String

    init(action: NameAction, submit: @escaping (String) -> Void) {
        self.action = action
        self.submit = submit
        _name = State(initialValue: action.name)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            Text(action.title).font(.headline)
            TextField("名称", text: $name)
                .onSubmit(confirm)
            HStack {
                Spacer()
                Button("取消", role: .cancel) { dismiss() }
                    .keyboardShortcut(.cancelAction)
                Button("确定", action: confirm)
                    .keyboardShortcut(.defaultAction)
                    .disabled(name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
        }
        .padding(24)
        .frame(width: 360)
    }

    private func confirm() {
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        submit(trimmed)
        dismiss()
    }
}

private struct FileInspector: View {
    let items: [FileItem]
    let versions: (FileItem) -> Void

    var body: some View {
        Form {
            if items.count == 1, let item = items.first {
                LabeledContent("名称", value: item.filename)
                LabeledContent("类型", value: item.isDirectory ? "文件夹" : item.fileType)
                if !item.isDirectory {
                    LabeledContent("大小", value: item.size.formatted(.byteCount(style: .file)))
                    LabeledContent("版本", value: String(item.currentVersion))
                    Button("版本历史…") { versions(item) }
                }
                if let modified = item.modified {
                    LabeledContent("修改时间", value: modified.formatted(date: .abbreviated, time: .shortened))
                }
            } else {
                Text(items.isEmpty ? "选择项目以查看详细信息" : "已选择 \(items.count) 个项目")
                    .foregroundStyle(.secondary)
            }
        }
        .formStyle(.grouped)
    }
}
