import SwiftUI

/// Home 模块主页面
struct HomeView: View {
    @StateObject private var viewModel = HomeViewModel()
    @State private var showFRPSettingsPage: Bool = false
    let onScrollCollapsedChanged: (Bool) -> Void

    init(onScrollCollapsedChanged: @escaping (Bool) -> Void = { _ in }) {
        self.onScrollCollapsedChanged = onScrollCollapsedChanged
    }

    var body: some View {
        ScrollView {
            VStack(spacing: 24) {
                // 1. 容量与状态卡片
                capacityCard
                statusCard
                
                // 3. 传输任务
                VStack(spacing: 12) {
                    sectionHeader(title: "近期传输", icon: "arrow.up.arrow.down") {
                        NotificationCenter.default.post(name: .switchTabToTransfers, object: nil)
                    }
                    
                    ScrollView(.horizontal, showsIndicators: false) {
                        HStack(spacing: 12) {
                            if viewModel.photoItems.isEmpty {
                                emptyHintCard(icon: "doc.text", text: "暂无传输任务")
                            } else {
                                ForEach(viewModel.photoItems) { item in
                                    VStack(alignment: .leading, spacing: 8) {
                                        RoundedRectangle(cornerRadius: 12)
                                            .fill(Color.gray.opacity(0.1))
                                            .frame(width: 140, height: 100)
                                            .clipped()
                                            .overlay(
                                                Image(systemName: "photo")
                                                    .font(.system(size: 24))
                                                    .foregroundStyle(.gray)
                                            )
                                        
                                        Text(item.title)
                                            .font(.footnote)
                                            .lineLimit(1)
                                        
                                        Text(item.capturedAt)
                                            .font(.caption2)
                                            .foregroundStyle(.secondary)
                                    }
                                    .frame(width: 140)
                                    .liquidGlassCard()
                                }
                            }
                        }
                        .padding(.horizontal, 1)
                    }
                }

                // 4. 文件分类
                VStack(spacing: 12) {
                    sectionHeader(title: "最近文件", icon: "doc.text") {
                        NotificationCenter.default.post(name: .switchTabToFiles, object: nil)
                    }

                    VStack(spacing: 10) {
                        if viewModel.fileItems.isEmpty {
                            emptyHintCard(icon: "doc", text: "暂无最近文件")
                        } else {
                            ForEach(viewModel.fileItems) { item in
                                HStack(spacing: 14) {
                                    fileIcon(for: item.name)

                                    VStack(alignment: .leading, spacing: 4) {
                                        Text(item.name)
                                            .font(.appBody)
                                            .lineLimit(1)
                                        Text("\(item.sizeText) · \(item.updatedAt)")
                                            .font(.appCaption)
                                            .foregroundStyle(.secondary)
                                    }

                                    Spacer()

                                    Image(systemName: "chevron.right")
                                        .font(.caption)
                                        .foregroundStyle(.secondary)
                                }
                                .liquidGlassCard()
                            }
                        }
                    }
                }

                if let errorMessage = viewModel.errorMessage {
                    Text(errorMessage)
                        .font(.footnote)
                        .foregroundStyle(.red)
                        .padding(.top, 4)
                }
            }
            .padding(.horizontal, 20)
            .padding(.vertical, 16)
        }
        .coordinateSpace(name: "home_scroll_area")
        .liquidGlassPage()
        .navigationTitle("首页")
        .navigationBarTitleDisplayMode(.large)
        .overlay {
            if viewModel.isLoading {
                ProgressView("加载中...")
                    .padding(14)
                    .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 12))
            }
        }
        .refreshable {
            await viewModel.reload()
        }
        .navigationDestination(isPresented: $showFRPSettingsPage) {
            FRPSettingsView()
        }
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button {
                    // 进入全局搜索页
                } label: {
                    Image(systemName: "magnifyingglass")
                        .font(.system(size: 15, weight: .semibold))
                        .foregroundStyle(.black)
                }
                .buttonStyle(.plain)
            }
        }
        .onAppear {
            viewModel.onAppear()
        }
    }
    
    // MARK: - Subviews
    
    /// 容量卡片
    private var capacityCard: some View {
        VStack(spacing: 16) {
            HStack {
                VStack(alignment: .leading, spacing: 4) {
                    Text("存储空间")
                        .font(.headline)
                        .foregroundStyle(.primary)
                    Text(viewModel.capacityInfoText)
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                Image(systemName: "cloud.fill")
                    .font(.system(size: 28))
                    .foregroundStyle(.blue)
            }
            
            GeometryReader { geo in
                ZStack(alignment: .leading) {
                    Capsule()
                        .fill(Color.gray.opacity(0.15))
                        .frame(height: 8)
                    
                    if let percentage = viewModel.capacityInfo.percentage, percentage > 0 {
                        // 模拟多色分段进度条（图片、视频、其他）
                        HStack(spacing: 0) {
                            Capsule()
                                .fill(Color.purple)
                                .frame(width: geo.size.width * CGFloat(percentage) * 0.4)
                            Rectangle()
                                .fill(Color.blue)
                                .frame(width: geo.size.width * CGFloat(percentage) * 0.3)
                            Capsule()
                                .fill(Color.orange)
                                .frame(width: geo.size.width * CGFloat(percentage) * 0.3)
                        }
                        .frame(height: 8)
                        .mask(Capsule().frame(width: max(0, geo.size.width * CGFloat(percentage))))
                    }
                }
            }
            .frame(height: 8)

            HStack(spacing: 16) {
                legendItem(color: .purple, title: "照片")
                legendItem(color: .blue, title: "视频")
                legendItem(color: .orange, title: "其他")
                Spacer()
            }
        }
        .liquidGlassCard()
    }

    private func legendItem(color: Color, title: String) -> some View {
        HStack(spacing: 4) {
            Circle()
                .fill(color)
                .frame(width: 6, height: 6)
            Text(title)
                .font(.caption2)
                .foregroundStyle(.secondary)
        }
    }

    /// FRP 概览卡片
    private var statusCard: some View {
        Button {
            showFRPSettingsPage = true
        } label: {
            HStack(spacing: 12) {
                VStack(alignment: .leading, spacing: 6) {
                    Text("FRP 设备")
                        .font(.headline)
                    Text("连接 \(viewModel.statusInfo.frpDeviceTotal) 个终端")
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity, alignment: .leading)

                VStack(alignment: .leading, spacing: 6) {
                    Text("连接状态")
                        .font(.headline)
                    HStack(spacing: 6) {
                        Circle()
                            .fill(viewModel.statusInfo.frpDeviceEnabled > 0 ? Color.green : Color.red)
                            .frame(width: 8, height: 8)
                        Text(viewModel.statusInfo.frpDeviceEnabled > 0 ? "已连接" : "断开连接")
                            .font(.subheadline)
                            .foregroundStyle(viewModel.statusInfo.frpDeviceEnabled > 0 ? .green : .red)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)

                VStack(alignment: .trailing, spacing: 2) {
                    Text("FRPC设置")
                        .font(.caption)
                        .foregroundStyle(.blue)
                }
            }
        }
        .buttonStyle(.plain)
        .liquidGlassCard()
    }

    /// 分类区标题行
    private func sectionHeader(title: String, icon: String, action: @escaping () -> Void) -> some View {
        HStack(spacing: 8) {
            Image(systemName: icon)
                .foregroundStyle(.primary)
            Text(title)
                .font(.headline)
            Spacer()
            
            Button("查看全部", action: action)
                .font(.caption)
                .foregroundStyle(.blue)
        }
        .padding(.horizontal, 4)
    }

    /// 简单的按类型匹配图标辅助函数
    private func fileIcon(for filename: String) -> some View {
        let ext = (filename as NSString).pathExtension.lowercased()
        let iconName: String
        let color: Color
        switch ext {
        case "jpg", "jpeg", "png", "heic", "webp", "gif":
            iconName = "photo.fill"
            color = .purple
        case "pdf":
            iconName = "doc.richtext.fill"
            color = .red
        case "mp4", "mov", "avi", "mkv":
            iconName = "play.rectangle.fill"
            color = .blue
        case "doc", "docx", "txt", "md":
            iconName = "doc.text.fill"
            color = .teal
        case "xls", "xlsx", "csv":
            iconName = "chart.bar.doc.horizontal.fill"
            color = .green
        case "ppt", "pptx":
            iconName = "chart.pie.doc.fill"
            color = .orange
        case "zip", "tar", "gz", "rar", "7z":
            iconName = "doc.zipper"
            color = .yellow
        default:
            iconName = "doc.fill"
            color = .gray
        }
        return Image(systemName: iconName)
            .foregroundStyle(color.opacity(0.8))
            .font(.system(size: 24))
    }

    private func emptyHintCard(icon: String, text: String) -> some View {
        HStack(spacing: 8) {
            Image(systemName: icon)
                .foregroundStyle(.secondary)
            Text(text)
                .font(.footnote)
                .foregroundStyle(.secondary)
            Spacer()
        }
        .liquidGlassCard()
    }
}

struct HomeView_Previews: PreviewProvider {
    static var previews: some View {
        NavigationView {
            HomeView()
        }
    }
}
