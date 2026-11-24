import SwiftUI
import AppKit

struct SharesView: View {
    let session: SessionState
    let file: FileItem?
    @Environment(\.dismiss) private var dismiss
    @State private var state: SharesState
    @State private var creating = false
    @State private var revoking: ShareLink?
    @State private var attempted: Set<Int64> = []
    @State private var writing = false
    @State private var error: String?
    @State private var notice: String?
    private let identity: TransferIdentity?
    private let origin: ServerOrigin?

    init(session: SessionState, file: FileItem? = nil) {
        self.session = session; self.file = file; identity = session.transferIdentity; origin = session.origin
        _state = State(initialValue: SharesState(session: session))
    }
    private var rows: [ShareLink] { state.items.filter { file == nil || $0.fileID == file?.id } }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text(file.map { "\($0.filename) 的分享" } ?? "我创建的分享").font(.title2).lineLimit(3)
            HStack {
                Button("刷新分享") { Task { await state.load() } }.disabled(state.busy || writing)
                if let file, file.workspaceID == nil {
                    Button("创建分享…") { creating = true }.disabled(state.busy || writing || !state.loaded || state.error != nil)
                }
                Spacer()
                if state.busy || writing { ProgressView().controlSize(.small) }
            }
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 14) {
                    ForEach(rows) { share in
                        VStack(alignment: .leading, spacing: 6) {
                            Text(file?.filename ?? share.fileName ?? (share.fileID == nil ? "原文件已移除" : "未提供文件名")).font(.headline)
                            Text("创建于 \(share.createdAt.formatted(date: .numeric, time: .shortened))").foregroundStyle(.secondary)
                            Text(share.expiresAt.map { "到期：\($0.formatted(date: .numeric, time: .shortened))" } ?? "不自动到期")
                            Text("\(share.requiresPasscode ? "需要提取码" : "无需提取码") · 已下载 \(share.downloadCount) 次\(share.maxDownloads == 0 ? "，不限次数" : "，上限 \(share.maxDownloads) 次")")
                            if share.expired { Text("已到期").foregroundStyle(.secondary) }
                            if share.exhausted { Text("下载次数已用尽").foregroundStyle(.secondary) }
                            HStack {
                                Button("复制链接") { copy(share.code) }.disabled(state.busy || writing || share.fileID == nil)
                                Button("撤销分享…", role: .destructive) { revoking = share }
                                    .disabled(state.busy || writing || attempted.contains(share.id))
                            }
                        }
                        Divider()
                    }
                    if state.loaded && !state.busy && state.error == nil && rows.isEmpty { Text("尚无分享记录。").foregroundStyle(.secondary) }
                    if let error = state.error { Text(error).foregroundStyle(.secondary) }
                    if let error { Text(error).foregroundStyle(.secondary) }
                    if let notice { Text(notice).foregroundStyle(.secondary) }
                    Text("只显示此账户创建的链接。链接不包含提取码；请另行提供。到期、次数和文件访问权限由服务器在访问时核对。")
                        .font(.callout).foregroundStyle(.secondary)
                }.frame(maxWidth: .infinity, alignment: .leading)
            }.frame(minHeight: 80, maxHeight: .infinity).layoutPriority(-1)
            if file != nil { HStack { Spacer(); Button("关闭") { dismiss() }.keyboardShortcut(.cancelAction).disabled(writing) } }
        }.padding(24)
        .navigationTitle("分享")
        .task { await state.load() }
        .onDisappear { state.cancel() }
        .interactiveDismissDisabled(writing)
        .sheet(isPresented: $creating, onDismiss: { Task { await state.load() } }) {
            if let file { CreateShareView(session: session, file: file) }
        }
        .confirmationDialog("撤销分享链接", isPresented: Binding(get: { revoking != nil }, set: { if !$0 { revoking = nil } }), presenting: revoking) { share in
            Button("确认撤销", role: .destructive) { Task { await revoke(share) } }
        } message: { share in
            Text("撤销「\(file?.filename ?? share.fileName ?? "此文件")」在 \(share.createdAt.formatted(date: .numeric, time: .shortened)) 创建的链接。撤销后不能继续访问；已开始的下载可能完成。")
        }
    }

    private func copy(_ code: String) {
        do {
            try session.requireTransferIdentity(identity)
            guard let origin else { throw ServerError.unauthorized }
            let url = try ShareContract.url(origin: origin, code: code)
            NSPasteboard.general.clearContents()
            guard NSPasteboard.general.setString(url.absoluteString, forType: .string) else { throw ServerError.unavailable }
            notice = "链接已复制，提取码未包含在内。"
        } catch { self.error = (error as? ServerError)?.localizedDescription ?? "无法复制链接。" }
    }

    private func revoke(_ share: ShareLink) async {
        guard !writing, attempted.insert(share.id).inserted else { return }
        writing = true; error = nil; notice = nil
        defer { writing = false }
        do {
            try await session.revokeShare(share, expectedIdentity: identity)
            try session.requireTransferIdentity(identity)
        } catch {
            if let rejection = error as? ServerError, [.notFound, .forbidden, .invalidOperation, .rateLimited].contains(rejection) { attempted.remove(share.id) }
            self.error = "未取得撤销成功回执，请刷新核对记录。\((error as? ServerError)?.localizedDescription ?? "连接未能完成。")"
        }
        await state.load()
    }
}

private struct CreateShareView: View {
    let session: SessionState
    let file: FileItem
    @Environment(\.dismiss) private var dismiss
    @State private var hours = "24"
    @State private var maximum = "10"
    @State private var protected = true
    @State private var passcode = ""
    @State private var busy = false
    @State private var submitted = false
    @State private var confirming = false
    @State private var created: CreatedShare?
    @State private var error: String?
    @State private var notice: String?
    private let identity: TransferIdentity?
    private let origin: ServerOrigin?

    init(session: SessionState, file: FileItem) {
        self.session = session; self.file = file; identity = session.transferIdentity; origin = session.origin
    }
    private var valid: Bool {
        guard let hours = Int(hours), (0...8760).contains(hours), let max = Int64(maximum), (0...1_000_000).contains(max) else { return false }
        return !protected || !passcode.isEmpty && ShareContract.validPasscode(passcode)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text(created == nil ? "创建分享链接" : "分享已创建").font(.title2)
            Text(file.filename).foregroundStyle(.secondary).lineLimit(3)
            if let created {
                Text(created.expiresAt.map { "到期：\($0.formatted(date: .numeric, time: .shortened))" } ?? "不自动到期")
                Text("\(created.requiresPasscode ? "需要提取码" : "无需提取码") · \(created.maxDownloads == 0 ? "不限下载次数" : "最多下载 \(created.maxDownloads) 次")")
                Button("复制分享链接") { copy(created) }
                Text("链接不含提取码。提取码不会保存在本应用，关闭后可在分享列表管理链接。")
                    .font(.callout).foregroundStyle(.secondary)
            } else {
                TextField("有效小时数（0 为不自动到期，最多 8760）", text: $hours).disabled(busy || submitted)
                TextField("下载次数上限（0 为不限，最多 1000000）", text: $maximum).disabled(busy || submitted)
                Toggle("需要提取码", isOn: $protected).disabled(busy || submitted)
                    .onChange(of: protected) { _, value in if !value { passcode = "" } }
                if protected { SecureField("6–72 个可见 ASCII 字符，不含空格", text: $passcode).disabled(busy || submitted) }
                Text("持有链接与所需提取码的人可以访问。每次成功开始下载消耗一次额度，分段请求分别计次；中断的下载也可能已计次。")
                    .font(.callout).foregroundStyle(.secondary)
            }
            if let error { Text(error).foregroundStyle(.secondary) }
            if let notice { Text(notice).foregroundStyle(.secondary) }
            HStack {
                if busy { ProgressView().controlSize(.small) }
                Spacer()
                Button("关闭") { dismiss() }.keyboardShortcut(.cancelAction).disabled(busy)
                if created == nil { Button("创建…") { confirming = true }.disabled(busy || submitted || !valid) }
            }
        }.padding(24).frame(width: 520)
        .interactiveDismissDisabled(busy)
        .onDisappear { passcode = ""; created = nil }
        .confirmationDialog("确认分享访问范围", isPresented: $confirming, titleVisibility: .visible) {
            Button("确认创建") { Task { await create() } }
        } message: {
            Text("文件：\(file.filename)。\(Int(hours) == 0 ? "不自动到期" : "\(Int(hours) ?? 0) 小时有效")；\(Int64(maximum) == 0 ? "不限下载次数" : "最多下载 \(Int64(maximum) ?? 0) 次")；\(protected ? "需要提取码" : "无需提取码")。")
        }
    }

    private func create() async {
        guard !busy, !submitted, valid, let hours = Int(hours), let maximum = Int64(maximum) else { return }
        busy = true; submitted = true; error = nil
        defer { busy = false; passcode = "" }
        do {
            let result = try await session.createShare(file: file, expireHours: hours, passcode: protected ? passcode : "", maxDownloads: maximum, expectedIdentity: identity)
            try session.requireTransferIdentity(identity)
            created = result
        } catch {
            if let rejection = error as? ServerError, [.invalidOperation, .forbidden, .notFound, .rateLimited].contains(rejection) {
                submitted = false; self.error = rejection.localizedDescription
            } else { self.error = "未取得创建成功回执，请关闭并刷新分享列表核对，避免重复创建。" }
        }
    }

    private func copy(_ created: CreatedShare) {
        do {
            try session.requireTransferIdentity(identity)
            guard let origin else { throw ServerError.unauthorized }
            let url = try ShareContract.url(origin: origin, code: created.code)
            NSPasteboard.general.clearContents()
            guard NSPasteboard.general.setString(url.absoluteString, forType: .string) else { throw ServerError.unavailable }
            notice = "链接已复制，提取码未包含在内。"
        } catch { self.error = (error as? ServerError)?.localizedDescription ?? "无法复制链接。" }
    }
}
