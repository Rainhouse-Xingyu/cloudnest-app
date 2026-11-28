import SwiftUI
import AppKit

struct AISearchView: View {
    let session: SessionState
    let transfers: TransferManager
    @State private var search: AISearchState
    @State private var index: AIIndexState
    @State private var section = Section.search
    @State private var rebuilding: AIIndexItem?
    private enum Section: String, CaseIterable { case search = "自然语言检索", index = "索引状态" }

    init(session: SessionState, transfers: TransferManager) {
        self.session = session
        self.transfers = transfers
        _search = State(initialValue: AISearchState(session: session))
        _index = State(initialValue: AIIndexState(session: session))
    }

    var body: some View {
        VStack(spacing: 0) {
            Picker("AI 功能", selection: $section) {
                ForEach(Section.allCases, id: \.self) { Text($0.rawValue).tag($0) }
            }
            .pickerStyle(.segmented)
            .padding()
            Divider()
            if section == .search { searchContent } else { indexContent }
        }
        .onDisappear { search.cancel() }
        .task(id: section) {
            if section == .index && !index.loaded { await index.load() }
            if section == .index { search.cancel() }
        }
        .confirmationDialog("重建此文件的索引？", isPresented: Binding(
            get: { rebuilding != nil }, set: { if !$0 { rebuilding = nil } }
        ), titleVisibility: .visible, presenting: rebuilding) { item in
            Button("重建索引") { Task { await index.rebuild(item) } }
        } message: { item in
            Text("将重新提取并索引“\(item.filename)”。完成前该文件可能暂时无法通过正文检索找到。")
        }
    }

    private var searchContent: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                TextField("描述要找的文件，例如：MySQL 数据迁移方案", text: $search.question)
                    .textFieldStyle(.roundedBorder)
                    .onSubmit { Task { await search.submit() } }
                    .accessibilityLabel("在有权访问的文件中检索")
                Button("检索") { Task { await search.submit() } }
                    .disabled(!search.canSubmit)
                    .keyboardShortcut(.return, modifiers: [.command])
                if search.busy { Button("取消") { search.cancel() } }
            }
            Text("搜索有权访问的文件；按相关性排序。正文结果取决于索引是否已完成。")
                .font(.callout).foregroundStyle(.secondary)
            if search.busy { ProgressView("正在检索") }
            if let error = search.error {
                Label(error, systemImage: "exclamationmark.circle").foregroundStyle(.secondary)
            }
            if let error = transfers.error {
                Label(error, systemImage: "exclamationmark.circle").foregroundStyle(.secondary)
            }
            if let response = search.response {
                retrievalSummary(response)
                List(response.items) { hit in
                    VStack(alignment: .leading, spacing: 7) {
                        HStack {
                            Label(hit.filename, systemImage: "doc.text").font(.headline)
                            Spacer()
                            Button("下载…") { download(hit) }
                                .buttonStyle(.borderless)
                                .accessibilityLabel("下载 \(hit.filename)")
                        }
                        if !hit.snippet.isEmpty { Text(hit.snippet).textSelection(.enabled) }
                        HStack {
                            Text(hit.matchSources.map(sourceName).joined(separator: " · "))
                            Spacer()
                            Text(hit.size.formatted(.byteCount(style: .file)))
                        }
                        .font(.caption).foregroundStyle(.secondary)
                    }
                    .padding(.vertical, 6)
                    .accessibilityElement(children: .contain)
                }
                .overlay {
                    if response.items.isEmpty {
                        ContentUnavailableView("没有匹配文件", systemImage: "magnifyingglass", description:
                            Text("尝试更具体的描述，或在索引状态中查看处理结果。"))
                    }
                }
                HStack {
                    Button("上一页") { Task { await search.go(to: search.page - 1) } }
                        .disabled(search.page <= 1 || search.busy)
                    Text("第 \(search.page) 页 · 本次候选 \(response.pagination.total) 项")
                        .font(.callout).foregroundStyle(.secondary)
                    Button("下一页") { Task { await search.go(to: search.page + 1) } }
                        .disabled(!search.hasNext || search.busy)
                }
                Text("分页期间文件或权限变化可能改变结果；重新检索可更新排序。")
                    .font(.caption).foregroundStyle(.secondary)
            } else if !search.searched {
                ContentUnavailableView("用描述查找文件", systemImage: "sparkle.magnifyingglass", description:
                    Text("输入文件内容、主题或照片场景，查看实际匹配片段与来源。"))
            } else if !search.busy && search.error == nil {
                ContentUnavailableView("检索已停止", systemImage: "magnifyingglass", description: Text("可修改描述后再次检索。"))
            }
            Spacer(minLength: 0)
        }
        .padding()
    }

    @ViewBuilder private func retrievalSummary(_ response: AISearchResponse) -> some View {
        Text("“\(search.submittedQuestion)” · \(response.retrieval.mode == "hybrid" ? "语义与关键词混合检索" : "关键词检索")")
            .font(.callout)
        if response.retrieval.mode == "keyword_fallback" || response.retrieval.warnings.contains("embedding_unavailable") {
            Label("语义模型暂不可用，本次仅使用关键词，可能遗漏相关内容。", systemImage: "exclamationmark.triangle")
                .font(.callout).foregroundStyle(.secondary)
        }
        ForEach(response.retrieval.warnings.filter { $0 != "embedding_unavailable" }, id: \.self) { warning in
            Text(warningName(warning)).font(.caption).foregroundStyle(.secondary)
        }
        if response.retrieval.windowLimited {
            Text("本次仅展示最相关的前 \(response.retrieval.windowSize) 项候选，请缩小描述以查找更多文件。")
                .font(.caption).foregroundStyle(.secondary)
        }
    }

    private var indexContent: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("此列表仅包含自己拥有的文件。已完成且与当前文件一致的索引才可用于正文检索。")
                .font(.callout).foregroundStyle(.secondary)
            HStack {
                Button("刷新状态", systemImage: "arrow.clockwise") { Task { await index.load(after: index.after) } }
                if index.after > 0 { Button("返回第一页") { Task { await index.load() } } }
                Spacer()
                if index.busy { ProgressView().controlSize(.small) }
                Button("下一页") { Task { await index.load(after: index.nextAfter) } }
                    .disabled(!index.hasMore)
            }
            .disabled(index.busy)
            if let error = index.error { Label(error, systemImage: "exclamationmark.circle").foregroundStyle(.secondary) }
            if let notice = index.notice { Text(notice).foregroundStyle(.secondary) }
            List(index.items) { item in
                HStack {
                    VStack(alignment: .leading, spacing: 6) {
                        Text(item.filename).font(.headline)
                        Text(indexStatus(item)).foregroundStyle(.secondary)
                        if !item.errorCode.isEmpty {
                            Text(indexError(item.errorCode)).font(.caption).textSelection(.enabled)
                        }
                    }
                    Spacer()
                    Button("重建…") { rebuilding = item }
                        .buttonStyle(.borderless)
                        .accessibilityLabel("重建 \(item.filename) 的索引")
                        .disabled(index.busy)
                }
                .padding(.vertical, 6)
                .accessibilityElement(children: .contain)
            }
            .overlay {
                if index.loaded && index.items.isEmpty && index.error == nil {
                    ContentUnavailableView("本页没有文件", systemImage: "doc.text.magnifyingglass")
                }
            }
        }
        .padding()
    }

    private func download(_ hit: AISearchHit) {
        guard let identity = session.transferIdentity else { return }
        let panel = NSSavePanel()
        panel.nameFieldStringValue = hit.filename
        panel.canCreateDirectories = true
        panel.begin { response in
            guard response == .OK, let destination = panel.url else { return }
            Task {
                do { try await transfers.enqueueDownload(hit, destination: destination, expectedIdentity: identity) }
                catch { transfers.report(error) }
            }
        }
    }

    private func indexStatus(_ item: AIIndexItem) -> String {
        if item.indexedCurrent { return "当前索引可用 · \(item.chunkCount) 个片段" }
        let status = ["not_indexed": "尚未索引", "queued": "等待处理", "running": "正在处理",
                      "retry": "等待重试", "failed": "处理失败", "done": "已有索引待更新"][item.status] ?? "未知状态：\(item.status)"
        return status + (item.sourceCurrent ? "" : " · 需更新索引")
    }

    private func sourceName(_ value: String) -> String {
        ["keyword": "关键词", "semantic": "语义", "rerank": "模型重排", "ip_address": "IP 地址"] [value] ?? value
    }

    private func indexError(_ value: String) -> String {
        ["model_unavailable": "模型暂不可用，请稍后刷新状态。", "extraction_busy": "提取服务繁忙，等待重试。",
         "vision_output_limit": "模型输出达到上限，未生成完整结果；调整模型后可重建。",
         "invalid_vision_response": "模型没有返回完整有效的分析，等待重试。",
         "extraction_limit": "文件超过当前提取限制。", "unsupported_type": "暂不支持此文件类型的正文索引。",
         "source_integrity": "文件完整性校验失败，请检查原文件。", "attempts_exhausted": "已达到自动重试次数，可核对后手动重建。"] [value]
            ?? "处理诊断：\(value)"
    }

    private func warningName(_ value: String) -> String {
        ["rerank_unavailable": "重排模型不可用，保留原始检索顺序。",
         "capture_time_uses_exif_camera_local_calendar": "拍摄日期按照片中相机本地日期筛选。"] [value] ?? "检索提示：\(value)"
    }
}
