import SwiftUI

/// Home 模块主页面
struct HomeView: View {
    @StateObject private var viewModel = HomeViewModel()
    @State private var didCollapseActionIcon: Bool = false
    let onScrollCollapsedChanged: (Bool) -> Void

    init(onScrollCollapsedChanged: @escaping (Bool) -> Void = { _ in }) {
        self.onScrollCollapsedChanged = onScrollCollapsedChanged
    }

    var body: some View {
        ScrollView {
            // 通过滚动偏移判断是否上滑到收缩阈值
            GeometryReader { proxy in
                Color.clear
                    .preference(
                        key: HomeScrollOffsetPreferenceKey.self,
                        value: proxy.frame(in: .named("home_scroll_area")).minY
                    )
            }
            .frame(height: 0)

            VStack(spacing: 24) {
                
                // 1. 容量与状态卡片
                capacityCard
                
                // 2. 快捷操作
                quickActionsView
                
                // 3. 照片分类
                VStack(spacing: 12) {
                    sectionHeader(title: "最近照片", icon: "photo.on.rectangle.angled")
                    
                    ScrollView(.horizontal, showsIndicators: false) {
                        HStack(spacing: 12) {
                            ForEach(viewModel.photoItems) { item in
                                VStack(alignment: .leading, spacing: 8) {
                                    RoundedRectangle(cornerRadius: 12)
                                        .fill(Color.gray.opacity(0.1))
                                        .frame(height: 100)
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
                        .padding(.horizontal, 1)
                    }
                }

                // 4. 文件分类
                VStack(spacing: 12) {
                    sectionHeader(title: "最近文件", icon: "doc.text")

                    VStack(spacing: 10) {
                        ForEach(viewModel.fileItems) { item in
                            HStack(spacing: 14) {
                                Image(systemName: "doc.fill")
                                    .foregroundStyle(Color.blue.opacity(0.8))
                                    .font(.system(size: 24))

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
            .padding(.horizontal, 20)
            .padding(.vertical, 16)
        }
        .coordinateSpace(name: "home_scroll_area")
        .liquidGlassPage()
        .onPreferenceChange(HomeScrollOffsetPreferenceKey.self) { minY in
            let shouldCollapse = minY < -22
            guard shouldCollapse != didCollapseActionIcon else { return }
            didCollapseActionIcon = shouldCollapse
            onScrollCollapsedChanged(shouldCollapse)
        }
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .topBarLeading) {
                Text("首页")
                    .font(.appTitle)
                    .fontWeight(.bold)
            }
            ToolbarItemGroup(placement: .topBarTrailing) {
                Button {
                    // 进入全局搜索页
                } label: {
                    Image(systemName: "magnifyingglass")
                }

                Button {
                    // 进入智能搜索页
                } label: {
                    Image(systemName: "sparkles")
                }
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
                    Text("已用 \(viewModel.capacityInfo.usedText) / \(viewModel.capacityInfo.totalText)")
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
                    
                    Capsule()
                        .fill(Color.blue)
                        .frame(width: max(0, geo.size.width * CGFloat(viewModel.capacityInfo.percentage)), height: 8)
                }
            }
            .frame(height: 8)
        }
        .liquidGlassCard()
    }
    
    /// 快捷操作组
    private var quickActionsView: some View {
        HStack(spacing: 0) {
            quickActionButton(icon: "square.and.arrow.up", title: "上传", color: .blue)
            Spacer()
            quickActionButton(icon: "folder.badge.plus", title: "新建", color: .blue)
            Spacer()
            quickActionButton(icon: "link", title: "分享", color: .blue)
            Spacer()
            quickActionButton(icon: "sparkles", title: "智能", color: .blue)
        }
        .padding(.horizontal, 10)
    }
    
    private func quickActionButton(icon: String, title: String, color: Color) -> some View {
        Button(action: {
            // TODO: Action
        }) {
            VStack(spacing: 8) {
                ZStack {
                    Circle()
                        .fill(color.opacity(0.1))
                        .frame(width: 50, height: 50)
                    Image(systemName: icon)
                        .font(.system(size: 20, weight: .medium))
                        .foregroundStyle(color)
                }
                Text(title)
                    .font(.caption)
                    .foregroundStyle(.primary)
            }
        }
    }

    /// 分类区标题行
    private func sectionHeader(title: String, icon: String) -> some View {
        HStack(spacing: 8) {
            Image(systemName: icon)
                .foregroundStyle(.primary)
            Text(title)
                .font(.headline)
            Spacer()
            
            Button("查看全部") { }
                .font(.caption)
                .foregroundStyle(.blue)
        }
        .padding(.horizontal, 4)
    }
}

private struct HomeScrollOffsetPreferenceKey: PreferenceKey {
    static var defaultValue: CGFloat = 0

    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) {
        value = nextValue()
    }
}

struct HomeView_Previews: PreviewProvider {
    static var previews: some View {
        NavigationView {
            HomeView()
        }
    }
}
