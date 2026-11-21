import SwiftUI
import AppKit

struct TransferListView: View {
    let transfers: TransferManager

    var body: some View {
        VStack(spacing: 0) {
            List(transfers.jobs) { job in
                HStack(alignment: .top, spacing: 12) {
                    Image(systemName: job.direction.isUpload ? "arrow.up.doc" : "arrow.down.doc")
                    VStack(alignment: .leading, spacing: 6) {
                        Text(job.resultName ?? job.name).lineLimit(1)
                        if let version = job.fileVersion {
                            Text("历史版本 \(version)").font(.caption).foregroundStyle(.secondary)
                        }
                        if job.direction == .versionUpload {
                            Text("发布至「\(job.versionTargetName ?? "文件")」· 基于版本 \(job.baseVersion ?? 0)")
                                .font(.caption).foregroundStyle(.secondary)
                            if let version = job.publishedVersion { Text("已发布为版本 \(version)").font(.caption).foregroundStyle(.secondary) }
                        }
                        if job.phase == .running && !transfers.legacyPreviewJobs.contains(job.id) {
                            TimelineView(.periodic(from: .now, by: 1)) { _ in
                                activity(job)
                            }
                        }
                        Text(status(job))
                            .font(.callout)
                            .foregroundStyle(.secondary)
                        if let error = job.error { Text(error).foregroundStyle(.secondary) }
                    }
                    Spacer(minLength: 12)
                    actions(job)
                }
                .padding(.vertical, 6)
            }
            .overlay {
                if transfers.jobs.isEmpty && transfers.restored {
                    ContentUnavailableView("没有传输任务", systemImage: "arrow.up.arrow.down")
                }
            }
            if let directory = transfers.legacyPreviewDirectory {
                HStack(alignment: .top) {
                    Label("旧版预览文件及记录已保留。需要重新预览或下载时，请从文件列表打开。", systemImage: "archivebox")
                    Spacer()
                    Button("在 Finder 中查看") { NSWorkspace.shared.activateFileViewerSelecting([directory]) }
                }
                .font(.callout)
                .foregroundStyle(.secondary)
                .padding(12)
            }
            if let error = transfers.error {
                HStack {
                    Label(error, systemImage: "exclamationmark.circle")
                    Spacer()
                    if !transfers.restored {
                        Button("重试") { Task { await transfers.restore() } }
                    }
                }
                .foregroundStyle(.secondary)
                .padding(12)
            }
        }
        .navigationTitle("传输")
        .task { await transfers.restore() }
    }

    @ViewBuilder private func activity(_ job: TransferRecord) -> some View {
        let activity = transfers.activity(job.id)
        if job.totalBytes > 0 {
            ProgressView(value: Double(job.confirmedBytes), total: Double(job.totalBytes))
        } else {
            ProgressView().controlSize(.small)
        }
        HStack {
            Text(activity?.stage.title ?? "正在准备文件")
            Spacer()
            if activity?.stage == .transferring {
                if let rate = activity?.bytesPerSecond {
                    Text("\(TransferTelemetry.formattedRate(rate)) /s")
                        .monospacedDigit()
                } else {
                    Text("正在测量速度")
                }
            }
        }
        .font(.caption)
        .foregroundStyle(.secondary)
        .help("最近 5 秒的传输速度。已确认进度需等待服务器回执或本地保存，因此可能滞后。")
    }

    @ViewBuilder private func actions(_ job: TransferRecord) -> some View {
        if transfers.legacyPreviewJobs.contains(job.id) {
            EmptyView()
        } else {
            switch job.phase {
            case .queued, .running:
                Button("暂停") { Task { await transfers.pause(job.id) } }
                Button("取消", role: .destructive) { Task { await transfers.cancel(job.id) } }
            case .paused, .failed:
                Button(job.cancelRequested ? "重试取消" : "继续") { Task { await transfers.resume(job.id) } }
                Button("取消", role: .destructive) { Task { await transfers.cancel(job.id) } }
            case .completed:
                if job.direction.isDownload {
                    Button("在 Finder 中显示") {
                        Task {
                            do { NSWorkspace.shared.activateFileViewerSelecting([try await transfers.resultURL(job.id)]) }
                            catch { transfers.report(error) }
                        }
                    }
                }
            case .pausing, .cancelling:
                ProgressView().controlSize(.small)
            case .cancelled:
                EmptyView()
            }
        }
    }

    private func status(_ job: TransferRecord) -> String {
        if transfers.legacyPreviewJobs.contains(job.id) { return "旧版预览 · 已保留" }
        let progress = "\(job.confirmedBytes.formatted(.byteCount(style: .file))) / \(job.totalBytes.formatted(.byteCount(style: .file)))"
        switch job.phase {
        case .queued: return "等待传输"
        case .running: return job.totalBytes == 0 ? "尚无已确认数据" : "已确认 \(progress)"
        case .pausing: return "正在暂停"
        case .paused: return "已暂停 · \(progress)"
        case .cancelling: return "正在取消"
        case .cancelled: return "已取消"
        case .failed: return "未完成 · \(progress)"
        case .completed: return "已完成 · \(job.totalBytes.formatted(.byteCount(style: .file)))"
        }
    }
}
