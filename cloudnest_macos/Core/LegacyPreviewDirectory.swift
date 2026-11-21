import Foundation
import Darwin

// Before the managed cache, previews were ordinary durable downloads below
// <transfer store>/previews. Location identifies a reserved legacy namespace,
// not permission to delete its contents. Presence uses lstat; path comparison
// resolves aliases but never enumerates, reads file contents or removes files.
struct LegacyPreviewDirectory: Sendable {
    let root: URL
    private let roots: [String]

    init(store: URL) {
        let store = store.standardizedFileURL
        root = store.appending(path: "previews", directoryHint: .isDirectory)
        roots = [root.path, store.resolvingSymlinksInPath().appending(path: "previews").path]
    }

    var isPresent: Bool {
        var info = stat()
        if lstat(root.path, &info) == 0 { return true }
        // Permission/IO errors are not evidence that historical files are absent.
        return errno != ENOENT
    }

    func containsBookmark(_ data: Data) -> Bool {
        var stale = false
        // Classification needs location only, never a sandbox access grant.
        // A previous signing identity can make scoped resolution fail. A stale
        // location may still identify retained data but must not enable writing.
        guard let directory = try? URL(resolvingBookmarkData: data, options: [.withoutUI, .withoutMounting],
                                      relativeTo: nil, bookmarkDataIsStale: &stale) else { return false }
        return contains(directory)
    }

    func contains(_ directory: URL) -> Bool {
        let paths = [directory.standardizedFileURL.path, directory.standardizedFileURL.resolvingSymlinksInPath().path]
        return paths.contains { path in roots.contains { path == $0 || path.hasPrefix($0 + "/") } }
    }
}
