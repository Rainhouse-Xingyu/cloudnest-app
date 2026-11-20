import SwiftUI

struct FileDestinationAction: Identifiable {
    let id = UUID()
    let ids: [Int64]
    let copy: Bool
}

struct FileDestinationSheet: View {
    let action: FileDestinationAction
    let submit: (Int64) -> Void
    @State private var browser: FileBrowserState
    @Environment(\.dismiss) private var dismiss

    init(session: SessionState, action: FileDestinationAction, submit: @escaping (Int64) -> Void) {
        self.action = action
        self.submit = submit
        _browser = State(initialValue: FileBrowserState(session: session, recycleBin: false))
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text(action.copy ? "复制到" : "移动到").font(.headline)
            HStack {
                Button("我的文件") { Task { await browser.navigate(to: 0) } }
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
                .disabled(browser.busy || !browser.loaded || browser.error != nil || action.ids.contains(browser.parentID))
            }
        }
        .padding(22)
        .frame(width: 520, height: 400)
        .task { await browser.reload() }
    }
}
