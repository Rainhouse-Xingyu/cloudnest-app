import SwiftUI

@MainActor
struct FileBrowserActions {
    var createFolder: (() -> Void)?
    var upload: (() -> Void)?
    var download: (() -> Void)?
    var preview: (() -> Void)?
    var versions: (() -> Void)?
    var rename: (() -> Void)?
    var move: (() -> Void)?
    var copy: (() -> Void)?
    var trash: (() -> Void)?
    var restore: (() -> Void)?
    var refresh: (() -> Void)?
    var inspector: (() -> Void)?
}

private struct FileBrowserActionsKey: FocusedValueKey {
    typealias Value = FileBrowserActions
}

private struct FileTableActionsKey: FocusedValueKey {
    typealias Value = FileBrowserActions
}

extension FocusedValues {
    var fileTableActions: FileBrowserActions? {
        get { self[FileTableActionsKey.self] }
        set { self[FileTableActionsKey.self] = newValue }
    }
    var fileBrowserActions: FileBrowserActions? {
        get { self[FileBrowserActionsKey.self] }
        set { self[FileBrowserActionsKey.self] = newValue }
    }
}

struct FileBrowserCommands: Commands {
    @FocusedValue(\.fileBrowserActions) private var actions
    @FocusedValue(\.fileTableActions) private var tableActions

    var body: some Commands {
        CommandGroup(after: .newItem) {
            Button("新建文件夹") { actions?.createFolder?() }
                .keyboardShortcut("n", modifiers: [.command, .shift])
                .disabled(actions?.createFolder == nil)
            Divider()
            Button("上传文件…") { actions?.upload?() }
                .keyboardShortcut("u", modifiers: [.command, .shift])
                .disabled(actions?.upload == nil)
            Button("下载所选项目…") { actions?.download?() }
                .keyboardShortcut("d", modifiers: [.command, .option])
                .disabled(actions?.download == nil)
            Button("快速查看") { actions?.preview?() }
                .keyboardShortcut("y")
                .disabled(actions?.preview == nil)
            Button("重新命名…") { actions?.rename?() }
                .disabled(actions?.rename == nil)
            Button("版本历史…") { actions?.versions?() }
                .disabled(actions?.versions == nil)
            Button("移动到…") { actions?.move?() }
                .disabled(actions?.move == nil)
            Button("复制到…") { actions?.copy?() }
                .disabled(actions?.copy == nil)
            Divider()
            Button("移入回收站") { tableActions?.trash?() }
                .keyboardShortcut(.delete, modifiers: .command)
                .disabled(tableActions?.trash == nil)
            Button("恢复所选项目") { actions?.restore?() }
                .disabled(actions?.restore == nil)
        }
        CommandGroup(after: .toolbar) {
            Button("刷新文件") { actions?.refresh?() }
                .keyboardShortcut("r")
                .disabled(actions?.refresh == nil)
            Button("显示或隐藏检查器") { actions?.inspector?() }
                .keyboardShortcut("i", modifiers: [.command, .option])
                .disabled(actions?.inspector == nil)
        }
    }
}
