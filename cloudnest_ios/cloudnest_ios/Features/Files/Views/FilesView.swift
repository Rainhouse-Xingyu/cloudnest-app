import SwiftUI

/// Files 模块主页面（浅色 LiquidGlass 风格）
struct FilesView: View {
    @StateObject private var viewModel = FilesViewModel()

    @State private var isSelectionMode: Bool = false
    @State private var selectedItems: Set<UUID> = []
    @State private var showDeleteConfirm: Bool = false

    private let folders: [FolderItem] = [
        .init(name: "我的文档", date: "2026-04-09"),
        .init(name: "下载", date: "2026-04-08"),
        .init(name: "归档", date: "2026-04-05"),
        .init(name: "项目", date: "2026-04-01")
    ]

    private let files: [FileItem] = [
        .init(name: "Vacation Photos.jpg", meta: "8.5 MB • 2026-04-09", icon: "photo.fill"),
        .init(name: "Presentation.pdf", meta: "4.2 MB • 2026-04-08", icon: "doc.richtext.fill")
    ]

    private let folderColumns = [
        GridItem(.flexible(), spacing: 14),
        GridItem(.flexible(), spacing: 14)
    ]

    var body: some View {
        ScrollView(showsIndicators: false) {
            VStack(spacing: 16) {
                headerBar
                breadcrumbBar
                personalSection

                LazyVGrid(columns: folderColumns, spacing: 14) {
                    ForEach(folders) { folder in
                        folderCard(folder)
                    }
                }

                VStack(spacing: 10) {
                    ForEach(files) { item in
                        fileRow(item)
                    }
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 10)
            .padding(.bottom, 24)
        }
        .overlay(alignment: .bottom) {
            if !selectedItems.isEmpty {
                bottomActionBar
            }
        }
        .alert("确认删除", isPresented: $showDeleteConfirm) {
            Button("取消", role: .cancel) { }
            Button("删除", role: .destructive) {
                withAnimation {
                    selectedItems.removeAll()
                    isSelectionMode = false
                }
                // TODO: 请求删除接口
            }
        } message: {
            Text("确定要删除选中的 \(selectedItems.count) 个项目吗？")
        }
        .liquidGlassPage()
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .principal) {
                Text("文件")
                    .font(.appTitle)
                    .fontWeight(.semibold)
            }
            ToolbarItemGroup(placement: .topBarTrailing) {
                Button {
                    // 全局/当前目录搜索
                } label: {
                    Image(systemName: "magnifyingglass")
                }
                
                Button {
                    // 上传文件
                } label: {
                    Image(systemName: "square.and.arrow.up")
                }
                
                Button {
                    // 新建文件夹
                } label: {
                    Image(systemName: "folder.badge.plus")
                }
                
                Menu {
                    Button(action: {}) {
                        Label("排序", systemImage: "arrow.up.arrow.down")
                    }
                    Button(action: {}) {
                        Label("筛选", systemImage: "line.3.horizontal.decrease.circle")
                    }
                    Button(action: {}) {
                        Label("回收站", systemImage: "trash")
                    }
                } label: {
                    Image(systemName: "ellipsis.circle")
                }
            }
        }
        .onAppear {
            viewModel.onAppear()
        }
    }

    private var headerBar: some View {
        HStack(spacing: 10) {
            Text("文件")
                .font(.system(size: 40, weight: .bold, design: .rounded))
                .foregroundStyle(.primary)

            Spacer()

            Button(isSelectionMode ? "取消" : "选择") {
                withAnimation(.spring()) {
                    isSelectionMode.toggle()
                    if !isSelectionMode {
                        selectedItems.removeAll()
                    }
                }
            }
            .font(.system(size: 18, weight: .semibold, design: .rounded))
            .foregroundStyle(Color.blue)

        }
        .padding(.horizontal, 2)
    }

    private var breadcrumbBar: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 8) {
                routeChip("Root", icon: "folder")
                Image(systemName: "chevron.right")
                    .foregroundStyle(.secondary)
                routeChip("Home")
                Image(systemName: "chevron.right")
                    .foregroundStyle(.secondary)
                routeChip("Team")
                Image(systemName: "chevron.right")
                    .foregroundStyle(.secondary)
                routeChip("Project")
            }
            .padding(.horizontal, 4)
        }
    }

    private var personalSection: some View {
        HStack {
            Text("PERSONAL")
                .font(.system(size: 15, weight: .bold, design: .rounded))
                .tracking(1.2)
                .foregroundStyle(.secondary)
            Spacer()
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 12)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 16, style: .continuous)
                .stroke(Color.white.opacity(0.56), lineWidth: 1)
        )
    }

    private func routeChip(_ text: String, icon: String? = nil) -> some View {
        HStack(spacing: 6) {
            if let icon {
                Image(systemName: icon)
            }
            Text(text)
        }
        .font(.system(size: 16, weight: .medium, design: .rounded))
        .foregroundStyle(Color.blue.opacity(0.92))
        .padding(.horizontal, 16)
        .padding(.vertical, 10)
        .background(.ultraThinMaterial, in: Capsule(style: .continuous))
        .overlay(
            Capsule(style: .continuous)
                .stroke(Color.white.opacity(0.52), lineWidth: 1)
        )
    }

    private func folderCard(_ item: FolderItem) -> some View {
        Button {
            if isSelectionMode {
                toggleSelection(item.id)
            } else {
                // 进入文件夹逻辑
            }
        } label: {
            VStack(alignment: .leading, spacing: 14) {
                RoundedRectangle(cornerRadius: 16, style: .continuous)
                .fill(Color.white.opacity(0.28))
                .frame(height: 92)
                .overlay {
                    Image(systemName: "folder")
                        .font(.system(size: 44, weight: .regular))
                        .foregroundStyle(
                            LinearGradient(
                                colors: [
                                    Color(red: 0.18, green: 0.57, blue: 1.0),
                                    Color(red: 0.22, green: 0.69, blue: 1.0)
                                ],
                                startPoint: .topLeading,
                                endPoint: .bottomTrailing
                            )
                        )
                }

            Text(item.name)
                .font(.system(size: 33, weight: .semibold, design: .rounded))
                .lineLimit(1)
                .minimumScaleFactor(0.62)
                .foregroundStyle(.primary)

            Text(item.date)
                .font(.appCaption)
                .foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(18)
        .background(
            RoundedRectangle(cornerRadius: 24, style: .continuous)
                .fill(
                    LinearGradient(
                        colors: [
                            Color(red: 0.82, green: 0.89, blue: 1.0).opacity(0.50),
                            Color(red: 0.88, green: 0.84, blue: 1.0).opacity(0.45)
                        ],
                        startPoint: .topLeading,
                        endPoint: .bottomTrailing
                    )
                )
                .background(.ultraThinMaterial)
        )
        .overlay(
            RoundedRectangle(cornerRadius: 24, style: .continuous)
                .stroke(selectedItems.contains(item.id) ? Color.blue.opacity(0.8) : Color.white.opacity(0.60), lineWidth: selectedItems.contains(item.id) ? 2.5 : 1)
        )
        .shadow(color: Color.blue.opacity(0.09), radius: 14, x: 0, y: 8)
        }
        .buttonStyle(.plain)
    }

    private func fileRow(_ item: FileItem) -> some View {
        Button {
            if isSelectionMode {
                toggleSelection(item.id)
            } else {
                // 预览文件逻辑
            }
        } label: {
            HStack(spacing: 12) {
                RoundedRectangle(cornerRadius: 12, style: .continuous)
                .fill(Color.white.opacity(0.34))
                .frame(width: 54, height: 54)
                .overlay {
                    Image(systemName: item.icon)
                        .font(.system(size: 22, weight: .semibold))
                        .foregroundStyle(Color.orange)
                }

            VStack(alignment: .leading, spacing: 4) {
                Text(item.name)
                    .font(.system(size: 20, weight: .semibold, design: .rounded))
                    .lineLimit(1)
                Text(item.meta)
                    .font(.appCaption)
                    .foregroundStyle(.secondary)
            }

            Spacer()

            Image(systemName: "chevron.right")
                .foregroundStyle(.secondary)
        }
        .padding(12)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 18, style: .continuous)
                .stroke(selectedItems.contains(item.id) ? Color.blue.opacity(0.8) : Color.white.opacity(0.56), lineWidth: selectedItems.contains(item.id) ? 2.5 : 1)
        )
        }
        .buttonStyle(.plain)
    }

    private func toggleSelection(_ id: UUID) {
        withAnimation(.spring(response: 0.2)) {
            if selectedItems.contains(id) {
                selectedItems.remove(id)
            } else {
                selectedItems.insert(id)
            }
        }
    }

    private var bottomActionBar: some View {
        HStack(spacing: 0) {
            actionButton(title: "下载", icon: "arrow.down.circle") {
                // 下载逻辑
            }
            Spacer()
            actionButton(title: "移动", icon: "folder") {
                // 移动逻辑
            }
            Spacer()
            actionButton(title: "复制", icon: "doc.on.doc") {
                // 复制逻辑
            }
            Spacer()
            actionButton(title: "分享", icon: "square.and.arrow.up") {
                // 分享逻辑
            }
            Spacer()
            actionButton(title: "删除", icon: "trash", color: .red) {
                showDeleteConfirm = true
            }
        }
        .padding(.horizontal, 24)
        .padding(.vertical, 16)
        .background(.ultraThinMaterial, in: Capsule(style: .continuous))
        .overlay(
            Capsule(style: .continuous)
                .stroke(Color.white.opacity(0.6), lineWidth: 1)
        )
        .shadow(color: Color.black.opacity(0.12), radius: 24, x: 0, y: 12)
        .padding(.horizontal, 16)
        .padding(.bottom, 24)
        .transition(.move(edge: .bottom).combined(with: .opacity))
    }

    private func actionButton(title: String, icon: String, color: Color = .blue, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            VStack(spacing: 6) {
                Image(systemName: icon)
                    .font(.system(size: 20, weight: .medium))
                Text(title)
                    .font(.system(size: 11, weight: .semibold))
            }
            .foregroundStyle(color)
            .frame(minWidth: 44)
        }
    }
}

private struct FolderItem: Identifiable {
    let id = UUID()
    let name: String
    let date: String
}

private struct FileItem: Identifiable {
    let id = UUID()
    let name: String
    let meta: String
    let icon: String
}

struct FilesView_Previews: PreviewProvider {
    static var previews: some View {
        FilesView()
    }
}
