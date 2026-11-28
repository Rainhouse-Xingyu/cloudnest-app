import SwiftUI

struct FileCollaboratorsView: View {
    let session: SessionState
    @Environment(\.dismiss) private var dismiss
    @State private var state: FileCollaboratorsState
    @State private var editor: Editor?
    @State private var removing: FileCollaborator?
    @State private var attemptedRemovals: Set<Int64> = []
    @State private var writing = false
    @State private var error: String?
    private let identity: TransferIdentity?
    private struct Editor: Identifiable { let id = UUID(); let original: FileCollaborator? }

    init(session: SessionState, file: FileItem) {
        self.session = session; identity = session.transferIdentity
        _state = State(initialValue: FileCollaboratorsState(session: session, file: file))
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("文件协作者").font(.title2)
            Text(state.file.filename).foregroundStyle(.secondary).lineLimit(3)
            HStack {
                Button("刷新") { Task { await state.load() } }.disabled(state.busy || writing)
                if state.canManage {
                    Button("添加协作者…") { editor = Editor(original: nil) }
                        .disabled(state.busy || writing || !state.loaded || state.error != nil)
                }
                Spacer()
                if state.busy || writing { ProgressView().controlSize(.small) }
            }
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 12) {
                    ForEach(state.items) { person in
                        VStack(alignment: .leading, spacing: 6) {
                            Text(person.username).font(.headline)
                            Text("用户 \(String(person.userID)) · \(person.permission.title)").foregroundStyle(.secondary)
                            if state.canManage && person.userID != state.file.userID {
                                HStack {
                                    Button("更改权限…") { editor = Editor(original: person) }.disabled(writing || state.busy)
                                    Button("撤销访问…", role: .destructive) { removing = person }
                                        .disabled(writing || state.busy || attemptedRemovals.contains(person.id))
                                }
                            }
                        }
                        Divider()
                    }
                    if state.loaded && !state.busy && state.error == nil && state.items.isEmpty {
                        Text("尚未添加协作者。").foregroundStyle(.secondary)
                    }
                    if let error = state.error { Text(error).foregroundStyle(.secondary) }
                    if let error { Text(error).foregroundStyle(.secondary) }
                    Text("仅文件所有者可管理授权。读写协作者可修改文件内容，但不能再授权其他账户。文件夹授权不会自动授予子项权限。")
                        .font(.callout).foregroundStyle(.secondary)
                }.frame(maxWidth: .infinity, alignment: .leading)
            }.frame(minHeight: 80, maxHeight: .infinity).layoutPriority(-1)
            HStack { Spacer(); Button("关闭") { dismiss() }.keyboardShortcut(.cancelAction).disabled(writing) }
        }.padding(24).frame(width: 520).frame(minHeight: 360, idealHeight: 500)
        .task { await state.load() }
        .onDisappear { state.cancel() }
        .sheet(item: $editor, onDismiss: { Task { await state.load() } }) { item in
            FileCollaboratorEditor(session: session, file: state.file, original: item.original)
        }
        .confirmationDialog("撤销文件访问", isPresented: Binding(get: { removing != nil }, set: { if !$0 { removing = nil } }), presenting: removing) { person in
            Button("确认撤销", role: .destructive) { Task { await remove(person) } }
        } message: { person in
            Text("撤销「\(person.username)」对「\(state.file.filename)」的\(person.permission.title)授权。已开始的下载可能完成。")
        }
    }

    private func remove(_ person: FileCollaborator) async {
        guard state.canManage, !writing, attemptedRemovals.insert(person.id).inserted else { return }
        writing = true; error = nil
        defer { writing = false }
        do {
            try await session.changeFileCollaborator(file: state.file, original: person, permission: nil, expectedIdentity: identity)
            try session.requireTransferIdentity(identity)
        } catch {
            if let rejection = error as? ServerError, [.conflict, .forbidden, .notFound, .invalidOperation, .rateLimited].contains(rejection) {
                attemptedRemovals.remove(person.id)
            }
            self.error = error as? ServerError == .conflict
                ? "授权已变化，请刷新核对后重新确认。"
                : "未取得撤销成功回执，请关闭并重新打开列表核对。\((error as? ServerError)?.localizedDescription ?? "连接未能完成。")"
        }
        await state.load()
    }
}

private struct FileCollaboratorEditor: View {
    let session: SessionState
    let file: FileItem
    let original: FileCollaborator?
    private let identity: TransferIdentity?
    @Environment(\.dismiss) private var dismiss
    @State private var username: String
    @State private var permission: FilePermission
    @State private var busy = false
    @State private var submitted = false
    @State private var error: String?
    @State private var confirming = false

    init(session: SessionState, file: FileItem, original: FileCollaborator?) {
        self.session = session; self.file = file; self.original = original; identity = session.transferIdentity
        _username = State(initialValue: original?.username ?? "")
        _permission = State(initialValue: original?.permission ?? .read)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text(original == nil ? "添加文件协作者" : "更改文件权限").font(.title2)
            Text(file.filename).foregroundStyle(.secondary).lineLimit(3)
            TextField("现有账户的用户名", text: $username).disabled(original != nil || busy || submitted)
            Picker("权限", selection: $permission) {
                ForEach(FilePermission.allCases, id: \.self) { Text($0.title).tag($0) }
            }.disabled(busy || submitted)
            Text(permission == .write ? "允许读取和修改此文件，包括提交新版本、重命名和删除。" : "允许读取、预览和下载此文件，不能修改。")
                .font(.callout).foregroundStyle(.secondary)
            if let error { Text(error).foregroundStyle(.secondary) }
            HStack {
                if busy { ProgressView().controlSize(.small) }
                Spacer()
                Button("关闭") { dismiss() }.keyboardShortcut(.cancelAction).disabled(busy)
                Button("保存…") { confirming = true }
                    .disabled(busy || submitted || username.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
        }.padding(24).frame(width: 500)
        .interactiveDismissDisabled(busy)
        .confirmationDialog("确认文件访问权限", isPresented: $confirming, titleVisibility: .visible) {
            Button("确认保存") { Task { await save() } }
        } message: {
            Text("文件：\(file.filename)。账户：\(username)。权限：\(permission.title)。\(original == nil ? "仅添加新协作者；已有授权需刷新后编辑。" : "授权已变化时拒绝覆盖。")")
        }
    }

    private func save() async {
        guard !busy, !submitted, file.canManageCollaborators(userID: identity?.userID) else { return }
        busy = true; submitted = true; error = nil
        defer { busy = false }
        do {
            if let original {
                try await session.changeFileCollaborator(file: file, original: original, permission: permission, expectedIdentity: identity)
            } else {
                try await session.addFileCollaborator(file: file, username: username, permission: permission, expectedIdentity: identity)
            }
            try session.requireTransferIdentity(identity)
            dismiss()
        } catch {
            if error as? ServerError == .conflict {
                self.error = "协作者已存在或授权已变化，请关闭并刷新后核对。"
            } else if let rejection = error as? ServerError, [.invalidOperation, .forbidden, .notFound, .rateLimited].contains(rejection) {
                submitted = false; self.error = rejection.localizedDescription
            } else { self.error = "未取得保存成功回执，请关闭并刷新核对，避免重复请求。" }
        }
    }
}
