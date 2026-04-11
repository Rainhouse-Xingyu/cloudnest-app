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

            VStack(spacing: 16) {
                // 顶部信息卡
                HStack(spacing: 10) {
                    Text(viewModel.title)
                        .font(.appBody)
                        .foregroundStyle(.primary)
                    Spacer()
                }
                .liquidGlassCard()
                
                // 照片分类

                
                ScrollView(.horizontal, showsIndicators: false) {

                        ForEach(viewModel.photoItems) { item in
                            VStack(alignment: .leading, spacing: 8) {
                                Image(systemName: "photo.fill")
                                    .font(.system(size: 20, weight: .semibold))
                                    .foregroundStyle(.cyan)
                                
                                Text(item.title)

                                    .lineLimit(1)
                                
                                Text(item.capturedAt)

                                    .foregroundStyle(.secondary)
                            }
                            .frame(width: 150, alignment: .leading)
                            .liquidGlassCard()
                        }
                    }
                    .padding(.horizontal, 1)

                // 文件分类
                sectionHeader(title: "文件", icon: "doc.text.fill")

                VStack(spacing: 10) {
                    ForEach(viewModel.fileItems) { item in
                        HStack(spacing: 10) {
//                            Image(systemName: "doc.fill")
//                                .foregroundStyle(.blue)
//                                .font(.system(size: 18, weight: .medium))

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
            .padding()
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
            // 左侧标题
            ToolbarItem(placement: .topBarLeading) {
                Text("照片")
                    .font(.appTitle)
                    .fontWeight(.semibold)
            }
        }
        .onAppear {
            viewModel.onAppear()
        }
    }

    /// 分类区标题行
    private func sectionHeader(title: String, icon: String) -> some View {
        HStack(spacing: 8) {
            Image(systemName: icon)
                .foregroundStyle(.blue)
            Text(title)
                .font(.appTitle)
            Spacer()
        }
        .padding(.horizontal, 2)
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
        HomeView()
    }
}
