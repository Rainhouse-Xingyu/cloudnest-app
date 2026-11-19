import Foundation
import Observation

@MainActor
@Observable
final class FileBrowserState {
    private(set) var items: [FileItem] = []
    private(set) var folders: [FileItem] = []
    private(set) var busy = false
    private(set) var error: String?
    private(set) var loaded = false
    var selection: Set<Int64> = []
    var filter = ""
    var sortOrder = [KeyPathComparator(\FileItem.filename, comparator: .localizedStandard)]
    let recycleBin: Bool
    private let session: SessionState

    var parentID: Int64 { folders.last?.id ?? 0 }
    var title: String { recycleBin ? "回收站" : folders.last?.filename ?? "我的文件" }
    var selectedItems: [FileItem] { visibleItems.filter { selection.contains($0.id) } }
    var visibleItems: [FileItem] {
        items.filter { filter.isEmpty || $0.filename.localizedStandardContains(filter) }.sorted(using: sortOrder)
    }

    init(session: SessionState, recycleBin: Bool) {
        self.session = session
        self.recycleBin = recycleBin
    }

    func reload() async { await load(folders: folders) }

    func open(_ item: FileItem) async {
        guard item.isDirectory, !recycleBin else { return }
        await load(folders: folders + [item])
    }

    func navigate(to depth: Int) async {
        guard (0...folders.count).contains(depth) else { return }
        await load(folders: Array(folders.prefix(depth)))
    }

    func createFolder(name: String) async {
        await mutate {
            try await self.session.createFolder(name: name, parentID: self.parentID)
        }
    }

    func rename(_ item: FileItem, name: String) async {
        await mutate { try await self.session.renameFile(id: item.id, name: name) }
    }

    func trashSelected() async {
        guard !busy, !recycleBin else { return }
        let ids = selectedItems.map(\.id)
        guard !ids.isEmpty else { return }
        busy = true
        error = nil
        do {
            let result = try await session.trashFiles(ids: ids)
            busy = false
            await reload()
            if !result.failed.isEmpty {
                selection = Set(result.failed.keys.compactMap(Int64.init)).intersection(Set(items.map(\.id)))
                error = "有 \(result.failed.count) 个项目未能移入回收站，请检查权限并刷新后重试。"
            }
        } catch {
            busy = false
            self.error = message(error)
        }
    }

    func restoreSelected() async {
        guard !busy, recycleBin else { return }
        // Restoring a parent also restores its descendants. Select one root per request.
        guard let item = selectedItems.first, selectedItems.count == 1 else { return }
        await mutate { try await self.session.restoreFile(id: item.id) }
    }

    private func load(folders next: [FileItem]) async {
        guard !busy else { return }
        busy = true
        error = nil
        defer { busy = false }
        do {
            let result = try await session.listFiles(parentID: next.last?.id ?? 0, recycleBin: recycleBin)
            try Task.checkCancellation()
            let changedDirectory = folders.map(\.id) != next.map(\.id)
            folders = next
            items = result
            loaded = true
            selection.formIntersection(Set(visibleItems.map(\.id)))
            if changedDirectory {
                selection.removeAll()
                filter = ""
            }
        } catch is CancellationError {
            return
        } catch {
            items = []
            selection.removeAll()
            loaded = true
            self.error = message(error)
        }
    }

    private func mutate(_ operation: () async throws -> Void) async {
        guard !busy else { return }
        busy = true
        error = nil
        do {
            try await operation()
            busy = false
            await reload()
        } catch {
            busy = false
            self.error = message(error)
        }
    }

    private func message(_ error: any Error) -> String {
        (error as? ServerError)?.localizedDescription ?? "无法完成文件操作，请检查连接后重试。"
    }
}
