import SwiftUI
import AppKit

struct FileVersionsSheet: View {
    let transfers: TransferManager
    let onRestored: () -> Void
    @State private var history: FileVersionState
    @State private var confirmation: VersionRestoreConfirmation?
    @Environment(\.dismiss) private var dismiss

    init(file: FileItem, session: SessionState, transfers: TransferManager, onRestored: @escaping () -> Void) {
        self.transfers = transfers
        self.onRestored = onRestored
        _history = State(initialValue: FileVersionState(file: file, session: session))
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack {
                VStack(alignment: .leading, spacing: 4) {
                    Text("版本历史").font(.headline)
                    Text(history.file.filename).lineLimit(2).textSelection(.enabled)
                    if let current = history.currentVersion {
                        Text("当前版本 \(current)").foregroundStyle(.secondary)
                    }
                }
                Spacer()
                if history.busy { ProgressView().controlSize(.small) }
                Button("刷新") { Task { await history.reload() } }.disabled(history.busy)
            }
            Table(history.items, selection: $history.selection) {
                TableColumn("版本") { item in
                    Text(item.version == history.currentVersion ? "\(item.version)（当前）" : String(item.version))
                }.width(min: 75, ideal: 100, max: 120)
                TableColumn("大小") { item in
                    Text(item.size.map { $0.formatted(.byteCount(style: .file)) } ?? "未知")
                }.width(min: 70, ideal: 85, max: 110)
                TableColumn("创建时间") { item in
                    if let date = item.created { Text(date, format: .dateTime.year().month().day().hour().minute()) }
                    else { Text("—") }
                }.width(min: 130, ideal: 150, max: 180)
                TableColumn("创建者") { item in Text(item.username.isEmpty ? "—" : item.username) }
                    .width(min: 70, ideal: 95, max: 130)
                TableColumn("说明") { item in Text(item.comment).lineLimit(2) }
                    .width(min: 110, ideal: 180)
            }
            .overlay {
                if history.loaded && history.items.isEmpty && history.error == nil {
                    ContentUnavailableView("没有已登记的版本", systemImage: "clock")
                }
            }
            if let error = history.error {
                Label(error, systemImage: "exclamationmark.circle").foregroundStyle(.secondary)
            }
            if let notice = history.notice { Text(notice).foregroundStyle(.secondary) }
            HStack {
                Button("下载所选版本…", action: download)
                    .disabled(history.busy || history.selected == nil)
                Button("恢复所选版本…") {
                    if let item = history.selected, let base = history.currentVersion {
                        confirmation = VersionRestoreConfirmation(item: item, base: base)
                    }
                }
                .disabled(history.busy || history.selected == nil || history.selected?.version == history.currentVersion)
                Spacer()
                Button("完成") { dismiss() }
                    .keyboardShortcut(.cancelAction)
                    .disabled(history.busy)
            }
        }
        .padding(20)
        .frame(minWidth: 640, idealWidth: 780, maxWidth: 1100, minHeight: 420, idealHeight: 520, maxHeight: 850)
        .interactiveDismissDisabled(history.busy)
        .task { await history.reload() }
        .confirmationDialog("将版本 \(confirmation?.item.version ?? 0) 恢复为当前文件？", isPresented: Binding(
            get: { confirmation != nil }, set: { if !$0 { confirmation = nil } }
        ), presenting: confirmation) { request in
            Button("恢复为新版本", role: .destructive) {
                Task {
                    if await history.restore(request.item, expectedCurrentVersion: request.base) { onRestored() }
                }
            }
        } message: { _ in Text("会新增一个版本，并保留已有历史。") }
    }

    private func download() {
        guard let version = history.selected else { return }
        let panel = NSSavePanel()
        let name = history.file.filename as NSString
        let suffix = name.pathExtension
        let base = suffix.isEmpty ? history.file.filename : name.deletingPathExtension
        panel.nameFieldStringValue = "\(base)（版本 \(version.version)）" + (suffix.isEmpty ? "" : ".\(suffix)")
        panel.canCreateDirectories = true
        panel.begin { result in
            guard result == .OK, let url = panel.url else { return }
            Task { await history.download(version, to: url, transfers: transfers) }
        }
    }
}

private struct VersionRestoreConfirmation {
    let item: FileVersionItem
    let base: Int
}
