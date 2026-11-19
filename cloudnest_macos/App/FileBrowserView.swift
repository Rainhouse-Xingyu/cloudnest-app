import SwiftUI

struct FileBrowserView: View {
    @State private var browser: FileBrowserState
    @State private var showingInspector = false
    @State private var naming: NameAction?
    @State private var confirmingTrash = false

    init(session: SessionState, recycleBin: Bool) {
        _browser = State(initialValue: FileBrowserState(session: session, recycleBin: recycleBin))
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
                    Task { await browser.open(item) }
                }
            }
            .onDeleteCommand {
                if !browser.recycleBin && !browser.selectedItems.isEmpty { confirmingTrash = true }
            }
            .overlay {
                if browser.busy && !browser.loaded { ProgressView("正在读取文件") }
                if browser.loaded && browser.items.isEmpty && browser.error == nil {
                    ContentUnavailableView(browser.recycleBin ? "回收站为空" : "此文件夹为空",
                                           systemImage: browser.recycleBin ? "trash" : "folder")
                }
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
                    Button {
                        naming = .create
                    } label: {
                        Label("新建文件夹", systemImage: "folder.badge.plus")
                    }
                    .keyboardShortcut("n", modifiers: [.command, .shift])
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
                .keyboardShortcut("r")
                Button {
                    showingInspector.toggle()
                } label: {
                    Label("检查器", systemImage: "sidebar.right")
                }
                .keyboardShortcut("i", modifiers: [.command, .option])
            }
        }
        .disabled(browser.busy)
        .inspector(isPresented: $showingInspector) {
            FileInspector(items: browser.selectedItems)
                .inspectorColumnWidth(min: 220, ideal: 260, max: 350)
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
    }

    private var breadcrumbs: some View {
        ScrollView(.horizontal) {
            HStack(spacing: 6) {
                Button("我的文件") { Task { await browser.navigate(to: 0) } }
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
                if ids.count == 1, let item = browser.items.first(where: { ids.contains($0.id) }) {
                    if item.isDirectory {
                        Button("打开") { Task { await browser.open(item) } }
                    }
                    Button("重新命名") { naming = .rename(item) }
                }
                Button("移入回收站", role: .destructive) {
                    browser.selection = ids
                    confirmingTrash = true
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

    var body: some View {
        Form {
            if items.count == 1, let item = items.first {
                LabeledContent("名称", value: item.filename)
                LabeledContent("类型", value: item.isDirectory ? "文件夹" : item.fileType)
                if !item.isDirectory {
                    LabeledContent("大小", value: item.size.formatted(.byteCount(style: .file)))
                    LabeledContent("版本", value: String(item.currentVersion))
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
