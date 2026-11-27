import SwiftUI
import AppKit
import UniformTypeIdentifiers

struct AuditView: View {
    let session: SessionState
    @State private var state: AuditState
    @State private var action = ""
    @State private var status = ""
    @State private var all = false
    @State private var useDates = false
    @State private var start = Calendar.current.startOfDay(for:Date())
    @State private var end = Date()
    @State private var inputError: String?
    @State private var filtering = false
    @State private var pending: Selection?
    private struct Selection { let export: AuditExportSelection; let destination: URL }

    init(session: SessionState) { self.session = session; _state = State(initialValue:AuditState(session:session)) }

    var body: some View {
        VStack(alignment:.leading,spacing:12) {
            if let page = state.page {
                Text("正在显示：\(summary(page.query))").font(.headline).lineLimit(2).help(summary(page.query))
                Text("当前范围 \(page.total) 条，每页最多 50 条。翻页保留当前范围；刷新加载较新记录，历史保留策略仍可能移除旧记录。显示时间采用此 Mac 时区，CSV 时间为 UTC。")
                    .font(.callout).foregroundStyle(.secondary).lineLimit(3)
                if page.items.isEmpty { Text("此页没有记录。").frame(maxWidth:.infinity,maxHeight:.infinity).foregroundStyle(.secondary) }
                else {
                    List(page.items) { entry in
                        DisclosureGroup {
                            LabeledContent("日志 ID",value:String(entry.id))
                            LabeledContent("账户 ID",value:entry.userID.map(String.init) ?? "系统或旧记录")
                            LabeledContent("文件 ID",value:entry.fileID.map(String.init) ?? "—")
                            LabeledContent("客户端地址",value:entry.clientIP.isEmpty ? "未记录" : entry.clientIP)
                            Text(entry.detail.isEmpty ? "无附加详情" : entry.detail).textSelection(.enabled)
                        } label: {
                            VStack(alignment:.leading,spacing:4) {
                                HStack { Text(entry.action); Spacer(); Text(entry.status) }
                                Text("#\(entry.id) · \(entry.createdAt.formatted(date:.numeric,time:.standard))").font(.caption).foregroundStyle(.secondary)
                            }.accessibilityElement(children:.combine)
                        }
                    }.frame(minHeight:80).layoutPriority(1)
                }
                HStack {
                    Button("上一页") { Task { await state.previous() } }.disabled(state.busy || page.nextAfterID == 0)
                    Button("下一页") { Task { await state.next() } }.disabled(state.busy || page.nextBeforeID == 0)
                    Spacer()
                    Text("历史日志不代表当前仍可访问对应文件。").font(.caption).foregroundStyle(.secondary)
                }
            } else if state.busy && !state.exporting { ProgressView("正在读取审计记录") }
            else { Text("应用筛选以读取记录。").foregroundStyle(.secondary); Spacer() }
            if state.exporting || state.receipt != nil || state.error != nil {
                ScrollView {
                    VStack(alignment:.leading,spacing:8) {
                        if state.exporting {
                            ProgressView(value:Double(state.received),total:Double(max(1,state.totalBytes)))
                            Text(state.cancelling ? "正在取消并清理…" : state.verifying ? "正在验证 CSV 完整性…" : "导出已接收 \(state.received.formatted(.byteCount(style:.file))) / \(state.totalBytes.formatted(.byteCount(style:.file)))")
                                .foregroundStyle(.secondary)
                        }
                        if let receipt = state.receipt {
                            Label("CSV 已验证并保存：\(receipt.rows) 条记录",systemImage:"checkmark.circle")
                            Text(receipt.url.path).textSelection(.enabled)
                            Text("SHA-256：\(receipt.sha256)").font(.caption.monospaced()).textSelection(.enabled)
                            if receipt.truncated { Text("此文件仅包含当前范围内最新的 5000 条记录。请缩小日期或操作筛选后另行导出其余记录。").foregroundStyle(.secondary) }
                        }
                        if let error = state.error { Text(error).foregroundStyle(.secondary) }
                    }.frame(maxWidth:.infinity,alignment:.leading)
                }.frame(height:state.receipt != nil ? 120 : state.exporting ? 72 : 44)
            }
        }.padding(16)
        .toolbar {
            Button("筛选审计记录",systemImage:"line.3.horizontal.decrease") { prepareFilter() }.disabled(state.busy)
            Button("刷新审计记录",systemImage:"arrow.clockwise") {
                if let query = state.query { Task { await state.load(query) } } else { apply() }
            }.disabled(state.busy)
            Button("导出 CSV…",systemImage:"square.and.arrow.down") { chooseDestination() }
                .disabled(state.busy || state.exportSelection == nil)
            if state.busy { Button("取消") { state.cancel() }.disabled(state.cancelling) }
        }
        .task { apply() }
        .onDisappear { state.discard() }
        .onChange(of:session.account?.isAdmin) {
            if session.account?.isAdmin != true { all = false; state.discard() }
        }
        .sheet(isPresented:$filtering) {
            VStack(alignment:.leading,spacing:16) {
                Text("筛选审计记录").font(.title2)
                TextField("操作名称（留空为全部，精确匹配）",text:$action)
                TextField("状态（如 success、failed；留空为全部）",text:$status)
                if session.account?.isAdmin == true { Toggle("所有账户（管理员）",isOn:$all) }
                else { Text("仅本账户记录").foregroundStyle(.secondary) }
                Toggle("按时间筛选",isOn:$useDates)
                if useDates {
                    DatePicker("开始时刻",selection:$start)
                    DatePicker("截止时刻（含）",selection:$end)
                }
                Text("按此 Mac 时区选择时刻。应用后会显示实际查询的秒级范围；CSV 时间为 UTC。")
                    .foregroundStyle(.secondary)
                if let error = inputError { Text(error).foregroundStyle(.secondary) }
                HStack {
                    Button("取消") { filtering = false; inputError = nil }.keyboardShortcut(.cancelAction)
                    Spacer()
                    Button("应用筛选") { apply() }
                }
            }.padding(24).frame(width:500)
        }
        .confirmationDialog("确认导出审计 CSV",isPresented:Binding(get:{ pending != nil },set:{if !$0 {pending=nil}}),presenting:pending) { selection in
            Button("确认导出") { Task { await state.export(selection.export,to:selection.destination) } }
        } message: { selection in
            Text("\(summary(selection.export.query))。此浏览范围最近查询有 \(selection.export.total) 条；最多导出最新 5000 条、16 MiB，完成后核验摘要。保存到：\(selection.destination.path)。已有文件不覆盖，重名时使用新名称。")
        }
    }

    private func apply() {
        do {
            let query = try AuditQuery(action:action,status:status,start:useDates ? start : nil,end:useDates ? end : nil,all:all)
            inputError = nil; filtering = false
            Task { await state.load(query) }
        } catch { inputError = "请检查操作名称、状态和时间范围，截止时刻须不早于开始。" }
    }
    private func prepareFilter() {
        if let query = state.query {
            action = query.action; status = query.status; all = query.all && session.account?.isAdmin == true
            useDates = query.start != nil || query.end != nil
            start = query.start ?? Calendar.current.startOfDay(for:Date()); end = query.end ?? Date()
        }
        inputError = nil; filtering = true
    }
    private func summary(_ query: AuditQuery) -> String {
        let scope = query.all ? "所有账户（管理员）" : "本账户"
        let filters = "\(query.action.isEmpty ? "全部操作" : query.action) · \(query.status.isEmpty ? "全部状态" : query.status)"
        let dates = query.start.map { " · 从 \($0.formatted(date:.numeric,time:.standard))" } ?? ""
        let end = query.end.map { " 至 \($0.formatted(date:.numeric,time:.standard))（含）" } ?? ""
        return "\(scope) · \(filters)\(dates)\(end)"
    }
    private func chooseDestination() {
        guard let selection = state.exportSelection else { return }
        let panel = NSSavePanel()
        panel.allowedContentTypes = [.commaSeparatedText]
        panel.nameFieldStringValue = "CloudNest-Audit.csv"
        panel.canCreateDirectories = true
        panel.begin { response in
            guard response == .OK, let url = panel.url else { return }
            pending = Selection(export:selection,destination:url)
        }
    }
}
