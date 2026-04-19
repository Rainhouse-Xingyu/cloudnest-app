import SwiftUI

struct AdminLogView: View {
    @StateObject private var viewModel = AdminLogViewModel()

    var body: some View {
        ScrollView(showsIndicators: false) {
            VStack(alignment: .leading, spacing: 14) {
                filterCard
                actionCard
                logListCard
                statusCard
            }
            .padding(.horizontal, 16)
            .padding(.top, 14)
            .padding(.bottom, 24)
        }
        .liquidGlassPage()
        .navigationTitle("日志与审计")
        .navigationBarTitleDisplayMode(.inline)
        .onAppear {
            viewModel.onAppear()
        }
        .refreshable {
            await viewModel.refresh()
        }
    }

    private var filterCard: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("筛选条件")
                .font(.system(size: 17, weight: .bold, design: .rounded))

            TextField("action，例如 upload / delete", text: $viewModel.actionInput)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled(true)
                .padding(.horizontal, 12)
                .padding(.vertical, 10)
                .background(Color.white.opacity(0.35), in: RoundedRectangle(cornerRadius: 10, style: .continuous))

            Picker("状态", selection: $viewModel.selectedStatus) {
                ForEach(OperationLogStatusFilter.allCases) { item in
                    Text(item.title).tag(item)
                }
            }
            .pickerStyle(.segmented)

            HStack(spacing: 8) {
                TextField("开始时间：YYYY-MM-DD HH:MM:SS", text: $viewModel.startTimeInput)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled(true)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 10)
                    .background(Color.white.opacity(0.35), in: RoundedRectangle(cornerRadius: 10, style: .continuous))

                TextField("结束时间：YYYY-MM-DD HH:MM:SS", text: $viewModel.endTimeInput)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled(true)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 10)
                    .background(Color.white.opacity(0.35), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
            }

            Toggle("管理员查看全部（all=1）", isOn: $viewModel.includeAll)
                .font(.footnote)
        }
        .padding(16)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 18, style: .continuous)
                .stroke(Color.white.opacity(0.6), lineWidth: 1)
        )
    }

    private var actionCard: some View {
        HStack(spacing: 10) {
            Button {
                Task { await viewModel.loadLogs(resetPage: true) }
            } label: {
                HStack {
                    if viewModel.isLoading {
                        ProgressView().tint(.white)
                    }
                    Text(viewModel.isLoading ? "查询中..." : "查询日志")
                }
                .frame(maxWidth: .infinity)
                .padding(.vertical, 12)
                .background(Color.blue, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                .foregroundStyle(.white)
            }
            .buttonStyle(.plain)

            Button {
                Task { await viewModel.exportCSV() }
            } label: {
                HStack {
                    if viewModel.isExporting {
                        ProgressView().tint(.white)
                    }
                    Text(viewModel.isExporting ? "导出中..." : "导出 CSV")
                }
                .frame(maxWidth: .infinity)
                .padding(.vertical, 12)
                .background(Color.green, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                .foregroundStyle(.white)
            }
            .buttonStyle(.plain)
        }
    }

    private var logListCard: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Text("日志列表")
                    .font(.system(size: 17, weight: .bold, design: .rounded))
                Spacer()
                Text("总数 \(viewModel.total)")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            }

            if viewModel.logs.isEmpty {
                Text(viewModel.isLoading ? "加载中..." : "暂无日志")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            } else {
                ForEach(viewModel.logs) { item in
                    VStack(alignment: .leading, spacing: 6) {
                        HStack {
                            Text("#\(item.id)")
                                .font(.system(size: 12, weight: .bold, design: .monospaced))
                            Text(item.action)
                                .font(.system(size: 13, weight: .semibold, design: .rounded))
                            Spacer()
                            Text(item.status)
                                .font(.caption)
                                .padding(.horizontal, 8)
                                .padding(.vertical, 4)
                                .background((item.status == "success" ? Color.green : Color.red).opacity(0.16), in: Capsule())
                        }

                        Text(item.detail.isEmpty ? "-" : item.detail)
                            .font(.footnote)
                            .foregroundStyle(.secondary)

                        Text("user_id=\(item.userId.map { String($0) } ?? "-")  file_id=\(item.fileId.map { String($0) } ?? "-")")
                            .font(.system(size: 12, weight: .medium, design: .monospaced))
                            .foregroundStyle(.secondary)

                        Text("IP: \(item.clientIP.isEmpty ? "-" : item.clientIP)  时间: \(item.createTime)")
                            .font(.system(size: 12, weight: .medium, design: .monospaced))
                            .foregroundStyle(.secondary)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(10)
                    .background(Color.white.opacity(0.28), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                }

                if viewModel.canLoadMore {
                    Button {
                        Task { await viewModel.loadNextPage() }
                    } label: {
                        HStack {
                            Spacer()
                            Text("加载更多")
                                .font(.footnote)
                                .padding(.vertical, 8)
                            Spacer()
                        }
                    }
                    .buttonStyle(.bordered)
                }
            }
        }
        .padding(16)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 18, style: .continuous)
                .stroke(Color.white.opacity(0.6), lineWidth: 1)
        )
    }

    @ViewBuilder
    private var statusCard: some View {
        if let success = viewModel.successMessage {
            VStack(alignment: .leading, spacing: 8) {
                Text(success)
                    .font(.footnote)
                    .foregroundStyle(.green)

                if let exportedFileURL = viewModel.exportedFileURL {
                    ShareLink(item: exportedFileURL) {
                        Label("分享导出的 CSV", systemImage: "square.and.arrow.up")
                            .font(.footnote)
                    }
                }
            }
            .padding(12)
            .background(Color.green.opacity(0.10), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
        }

        if let error = viewModel.errorMessage {
            Text(error)
                .font(.footnote)
                .foregroundStyle(.red)
                .padding(12)
                .background(Color.red.opacity(0.10), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
        }
    }
}

struct AdminLogView_Previews: PreviewProvider {
    static var previews: some View {
        NavigationStack {
            AdminLogView()
        }
    }
}
