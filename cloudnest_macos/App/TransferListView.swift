import SwiftUI
import AppKit

struct TransferListView: View {
    let transfers: TransferManager

    var body: some View {
        VStack(spacing: 0) {
            List(transfers.jobs) { job in
                HStack(alignment: .top, spacing: 12) {
                    Image(systemName: job.direction == .upload ? "arrow.up.doc" : "arrow.down.doc")
                    VStack(alignment: .leading, spacing: 6) {
                        Text(job.resultName ?? job.name).lineLimit(1)
                        if job.phase == .running {
                            ProgressView(value: Double(job.confirmedBytes), total: Double(max(1, job.totalBytes)))
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

    @ViewBuilder private func actions(_ job: TransferRecord) -> some View {
        switch job.phase {
        case .queued, .running:
            Button("暂停") { Task { await transfers.pause(job.id) } }
            Button("取消", role: .destructive) { Task { await transfers.cancel(job.id) } }
        case .paused, .failed:
            Button(job.cancelRequested ? "重试取消" : "继续") { Task { await transfers.resume(job.id) } }
            Button("取消", role: .destructive) { Task { await transfers.cancel(job.id) } }
        case .completed:
            if job.direction == .download {
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

    private func status(_ job: TransferRecord) -> String {
        let progress = "\(job.confirmedBytes.formatted(.byteCount(style: .file))) / \(job.totalBytes.formatted(.byteCount(style: .file)))"
        switch job.phase {
        case .queued: return "等待传输"
        case .running: return job.totalBytes == 0 ? "正在准备文件" : progress
        case .pausing: return "正在暂停"
        case .paused: return "已暂停 · \(progress)"
        case .cancelling: return "正在取消"
        case .cancelled: return "已取消"
        case .failed: return "未完成 · \(progress)"
        case .completed: return "已完成 · \(job.totalBytes.formatted(.byteCount(style: .file)))"
        }
    }
}
