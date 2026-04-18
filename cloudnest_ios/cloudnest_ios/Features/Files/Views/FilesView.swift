import SwiftUI
import UniformTypeIdentifiers
import QuickLook
import AVKit
import PhotosUI
import UIKit

private enum FilesLayoutStyle: String {
    case icon
    case list
}

/// Files 模块主页面（浅色 LiquidGlass 风格）
struct FilesView: View {
    @StateObject private var viewModel = FilesViewModel()

    @State private var isSelectionMode: Bool = false
    @State private var selectedItems: Set<Int64> = []
    @State private var showDeleteConfirm: Bool = false
    @State private var showFileImporter: Bool = false
    @State private var showUploadSourceDialog: Bool = false
    @State private var showPhotoPicker: Bool = false
    @State private var expandedTreeNodes: Set<Int64> = []
    @AppStorage("files.layout.style") private var layoutStyleRaw: String = FilesLayoutStyle.icon.rawValue

    private let folderColumns = [
        GridItem(.flexible(), spacing: 14),
        GridItem(.flexible(), spacing: 14)
    ]

    var body: some View {
        ScrollView(showsIndicators: false) {
            VStack(spacing: 16) {
                headerBar
                breadcrumbBar
                directoryTreeSection
                personalSection

                if viewModel.isLoading {
                    ProgressView("加载中...")
                        .frame(maxWidth: .infinity, minHeight: 200)
                } else if let errorMessage = viewModel.errorMessage {
                    VStack {
                        Text("获取列表失败")
                            .font(.headline)
                        Text(errorMessage)
                            .font(.subheadline)
                            .foregroundColor(.red)
                        Button("重试") {
                            Task { await viewModel.loadData() }
                        }
                        .padding()
                    }
                    .frame(maxWidth: .infinity, minHeight: 200)
                } else {
                    if currentLayoutStyle == .icon {
                        if !viewModel.folders.isEmpty {
                            LazyVGrid(columns: folderColumns, spacing: 14) {
                                ForEach(viewModel.folders) { folder in
                                    folderCard(folder)
                                }
                            }
                        }

                        if !viewModel.files.isEmpty {
                            LazyVGrid(columns: folderColumns, spacing: 14) {
                                ForEach(viewModel.files) { item in
                                    fileIconCard(item)
                                }
                            }
                        }
                    } else if !viewModel.files.isEmpty {
                        VStack(spacing: 10) {
                            ForEach(viewModel.files) { item in
                                fileRow(item)
                            }
                        }
                    }
                    
                    if viewModel.folders.isEmpty && viewModel.files.isEmpty {
                        VStack(spacing: 16) {
                            Image(systemName: "folder.badge.questionmark")
                                .font(.system(size: 60))
                                .foregroundColor(.secondary)
                            Text("空空如也")
                                .foregroundColor(.secondary)
                        }
                        .frame(maxWidth: .infinity, minHeight: 200)
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
        .sheet(isPresented: $showPhotoPicker) {
            PhotoLibraryPicker(
                onPicked: { data, originalName in
                    let filename = buildPhotoFilename(originalName: originalName, data: data)
                    showPhotoPicker = false
                    Task {
                        await viewModel.uploadPhotoData(data, filename: filename)
                    }
                },
                onError: { message in
                    showPhotoPicker = false
                    viewModel.errorMessage = message
                }
            )
            .ignoresSafeArea()
        }
        .sheet(isPresented: $showFileImporter) {
            DocumentFilePicker(
                onPicked: { url in
                    showFileImporter = false
                    Task {
                        await viewModel.uploadFile(url: url)
                    }
                },
                onError: { message in
                    showFileImporter = false
                    viewModel.errorMessage = message
                }
            )
            .ignoresSafeArea()
        }
        .confirmationDialog("选择上传来源", isPresented: $showUploadSourceDialog, titleVisibility: .visible) {
            Button("照片 App") {
                showPhotoPicker = true
            }
            Button("文件 App") {
                showFileImporter = true
            }
            Button("取消", role: .cancel) { }
        }
        .overlay {
            if viewModel.isUploading {
                VStack(spacing: 16) {
                    ProgressView(value: viewModel.uploadProgress)
                        .progressViewStyle(.linear)
                        .padding(.horizontal, 40)
                    Text("上传中... \(Int(viewModel.uploadProgress * 100))%")
                        .font(.appBody)
                        .foregroundColor(.primary)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(.ultraThinMaterial)
            }
        }
        .sheet(item: $viewModel.previewDocument) { doc in
            FileQuickLookPreview(url: doc.url)
        }
        .sheet(item: $viewModel.livePlayerItem) { item in
            LivePhotoPlayerView(url: item.url)
        }
        .alert("下载结果", isPresented: Binding(
            get: { viewModel.downloadMessage != nil },
            set: { newValue in
                if !newValue {
                    viewModel.downloadMessage = nil
                }
            }
        )) {
            Button("确定", role: .cancel) {
                viewModel.downloadMessage = nil
            }
        } message: {
            Text(viewModel.downloadMessage ?? "")
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
                    showUploadSourceDialog = true
                } label: {
                    Image(systemName: "square.and.arrow.up")
                }
                .disabled(viewModel.isUploading)

                Button {
                    toggleLayoutStyle()
                } label: {
                    Image(systemName: currentLayoutStyle == .icon ? "list.bullet" : "square.grid.2x2")
                }
                
                Button {
                    // 新建文件夹
                } label: {
                    Image(systemName: "folder.badge.plus")
                }
                
                Menu {
                    Button(action: {
                        Task { await viewModel.loadData() }
                    }) {
                        Label("刷新", systemImage: "arrow.clockwise")
                    }
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
                ForEach(Array(viewModel.currentPath.enumerated()), id: \.offset) { index, node in
                    Button {
                        Task {
                            await viewModel.navigateToPath(index)
                        }
                    } label: {
                        routeChip(node.name, icon: index == 0 ? "folder" : nil)
                    }
                    .buttonStyle(.plain)

                    if index < viewModel.currentPath.count - 1 {
                        Image(systemName: "chevron.right")
                            .foregroundStyle(.secondary)
                    }
                }
            }
            .padding(.horizontal, 4)
        }
    }

    private var directoryTreeSection: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Text("目录树")
                    .font(.headline)
                Spacer()
                if viewModel.isTreeLoading {
                    ProgressView()
                }
            }

            if viewModel.treeNodes.isEmpty {
                Text("暂无目录")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            } else {
                VStack(alignment: .leading, spacing: 6) {
                    ForEach(viewModel.treeNodes) { node in
                        treeNodeView(node: node, level: 0)
                    }
                }
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 12)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 16, style: .continuous)
                .stroke(Color.white.opacity(0.56), lineWidth: 1)
        )
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

    private func formatFileSize(_ bytes: Int64) -> String {
        let formatter = ByteCountFormatter()
        formatter.allowedUnits = [.useAll]
        formatter.countStyle = .file
        return formatter.string(fromByteCount: bytes)
    }

    private func getFileIcon(type: String) -> String {
        let typeLower = type.lowercased()
        if typeLower.contains("pdf") { return "doc.richtext.fill" }
        if typeLower.contains("image") || typeLower.contains("jpg") || typeLower.contains("png") { return "photo.fill" }
        if typeLower.contains("video") || typeLower.contains("mp4") { return "play.rectangle.fill" }
        if typeLower.contains("audio") || typeLower.contains("mp3") { return "headphones" }
        if typeLower.contains("zip") || typeLower.contains("tar") { return "doc.zipper" }
        return "doc.fill"
    }

    private func formatDate(_ isoString: String) -> String {
        // 后端 datetime `2026-04-09 12:34:56` 简单截取日期部分展示
        let components = isoString.components(separatedBy: " ")
        if let datePart = components.first {
            return datePart
        }
        return isoString
    }

    private func folderCard(_ item: FileItemDTO) -> some View {
        Button {
            if isSelectionMode {
                toggleSelection(item.id)
            } else {
                // 进入文件夹逻辑
                Task {
                    await viewModel.enterFolder(item)
                }
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

            Text(item.filename)
                .font(.system(size: 33, weight: .semibold, design: .rounded))
                .lineLimit(1)
                .minimumScaleFactor(0.62)
                .foregroundStyle(.primary)

            Text(formatDate(item.updateTime))
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

    private func fileRow(_ item: FileItemDTO) -> some View {
        HStack(spacing: 12) {
            RoundedRectangle(cornerRadius: 12, style: .continuous)
                .fill(Color.white.opacity(0.34))
                .frame(width: 54, height: 54)
                .overlay {
                    ZStack(alignment: .topLeading) {
                        if let thumb = viewModel.thumbnail(for: item.id) {
                            Image(uiImage: thumb)
                                .resizable()
                                .scaledToFill()
                                .frame(width: 50, height: 50)
                                .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
                        } else {
                            Image(systemName: getFileIcon(type: item.fileType))
                                .font(.system(size: 22, weight: .semibold))
                                .foregroundStyle(Color.orange)
                        }

                        if viewModel.isLivePhoto(item.id) {
                            Text("LIVE")
                                .font(.system(size: 9, weight: .bold, design: .rounded))
                                .foregroundStyle(.white)
                                .padding(.horizontal, 4)
                                .padding(.vertical, 2)
                                .background(Color.red, in: Capsule(style: .continuous))
                                .offset(x: -16, y: -16)
                        }
                    }
                }

            VStack(alignment: .leading, spacing: 4) {
                Text(item.filename)
                    .font(.system(size: 20, weight: .semibold, design: .rounded))
                    .lineLimit(1)
                Text("\(formatFileSize(item.size)) • \(formatDate(item.updateTime))")
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
        .contentShape(RoundedRectangle(cornerRadius: 18, style: .continuous))
        .onTapGesture {
            if isSelectionMode {
                toggleSelection(item.id)
            } else {
                Task {
                    await viewModel.openPreview(for: item)
                }
            }
        }
        .onLongPressGesture(minimumDuration: 0.45) {
            guard !isSelectionMode, viewModel.isLivePhoto(item.id) else { return }
            Task {
                await viewModel.playLivePhoto(for: item)
            }
        }
    }

    private func fileIconCard(_ item: FileItemDTO) -> some View {
        Button {
            if isSelectionMode {
                toggleSelection(item.id)
            } else {
                Task { await viewModel.openPreview(for: item) }
            }
        } label: {
            VStack(alignment: .leading, spacing: 12) {
                RoundedRectangle(cornerRadius: 16, style: .continuous)
                    .fill(Color.white.opacity(0.28))
                    .frame(height: 92)
                    .overlay {
                        ZStack(alignment: .topLeading) {
                            if let thumb = viewModel.thumbnail(for: item.id) {
                                Image(uiImage: thumb)
                                    .resizable()
                                    .scaledToFill()
                                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                                    .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
                                    .padding(8)
                            } else {
                                Image(systemName: getFileIcon(type: item.fileType))
                                    .font(.system(size: 38, weight: .semibold))
                                    .foregroundStyle(Color.orange)
                            }

                            if viewModel.isLivePhoto(item.id) {
                                Text("LIVE")
                                    .font(.system(size: 9, weight: .bold, design: .rounded))
                                    .foregroundStyle(.white)
                                    .padding(.horizontal, 5)
                                    .padding(.vertical, 2)
                                    .background(Color.red, in: Capsule(style: .continuous))
                                    .padding(8)
                            }
                        }
                    }

                Text(item.filename)
                    .font(.system(size: 18, weight: .semibold, design: .rounded))
                    .lineLimit(1)
                    .foregroundStyle(.primary)

                Text(formatDate(item.updateTime))
                    .font(.appCaption)
                    .foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(14)
            .background(
                RoundedRectangle(cornerRadius: 20, style: .continuous)
                    .fill(.ultraThinMaterial)
            )
            .overlay(
                RoundedRectangle(cornerRadius: 20, style: .continuous)
                    .stroke(selectedItems.contains(item.id) ? Color.blue.opacity(0.8) : Color.white.opacity(0.56), lineWidth: selectedItems.contains(item.id) ? 2.5 : 1)
            )
        }
        .buttonStyle(.plain)
        .onLongPressGesture(minimumDuration: 0.45) {
            guard !isSelectionMode, viewModel.isLivePhoto(item.id) else { return }
            Task { await viewModel.playLivePhoto(for: item) }
        }
    }

    private var currentLayoutStyle: FilesLayoutStyle {
        FilesLayoutStyle(rawValue: layoutStyleRaw) ?? .icon
    }

    private func toggleLayoutStyle() {
        layoutStyleRaw = (currentLayoutStyle == .icon) ? FilesLayoutStyle.list.rawValue : FilesLayoutStyle.icon.rawValue
    }

    private func buildPhotoFilename(originalName: String?, data: Data) -> String {
        if let originalName, !originalName.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            return originalName
        }

        let ext: String
        if data.starts(with: [0xFF, 0xD8, 0xFF]) {
            ext = "jpg"
        } else if data.starts(with: [0x89, 0x50, 0x4E, 0x47]) {
            ext = "png"
        } else if data.starts(with: [0x47, 0x49, 0x46, 0x38]) {
            ext = "gif"
        } else {
            ext = "jpg"
        }

        let formatter = DateFormatter()
        formatter.dateFormat = "yyyyMMdd_HHmmss"
        return "photo_\(formatter.string(from: Date())).\(ext)"
    }

    private func toggleSelection(_ id: Int64) {
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
                let allItems = viewModel.folders + viewModel.files
                let toDownload = allItems.filter { selectedItems.contains($0.id) }
                Task {
                    await viewModel.download(items: toDownload)
                }
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

    private func treeNodeView(node: FileTreeNode, level: Int) -> AnyView {
        AnyView(
            VStack(alignment: .leading, spacing: 4) {
                HStack(spacing: 6) {
                    if !node.children.isEmpty {
                        Button {
                            withAnimation(.easeInOut(duration: 0.2)) {
                                if expandedTreeNodes.contains(node.id) {
                                    expandedTreeNodes.remove(node.id)
                                } else {
                                    expandedTreeNodes.insert(node.id)
                                }
                            }
                        } label: {
                            Image(systemName: expandedTreeNodes.contains(node.id) ? "chevron.down" : "chevron.right")
                                .font(.caption2)
                                .foregroundStyle(.secondary)
                        }
                        .buttonStyle(.plain)
                    } else {
                        Image(systemName: "circle.fill")
                            .font(.system(size: 5))
                            .foregroundStyle(.secondary)
                    }

                    Button {
                        Task {
                            await viewModel.enterFolderFromTree(folderId: node.id)
                        }
                    } label: {
                        HStack(spacing: 6) {
                            Image(systemName: "folder")
                                .foregroundStyle(.blue)
                            Text(node.name)
                                .font(.footnote)
                                .foregroundStyle(.primary)
                        }
                    }
                    .buttonStyle(.plain)

                    Spacer()
                }
                .padding(.leading, CGFloat(level) * 14)

                if expandedTreeNodes.contains(node.id), !node.children.isEmpty {
                    ForEach(node.children) { child in
                        treeNodeView(node: child, level: level + 1)
                    }
                }
            }
        )
    }
}

struct FilesView_Previews: PreviewProvider {
    static var previews: some View {
        FilesView()
    }
}

private struct FileQuickLookPreview: UIViewControllerRepresentable {
    let url: URL

    func makeUIViewController(context: Context) -> QLPreviewController {
        let controller = QLPreviewController()
        controller.dataSource = context.coordinator
        return controller
    }

    func updateUIViewController(_ uiViewController: QLPreviewController, context: Context) {
        context.coordinator.url = url
        uiViewController.reloadData()
    }

    func makeCoordinator() -> Coordinator {
        Coordinator(url: url)
    }

    final class Coordinator: NSObject, QLPreviewControllerDataSource {
        var url: URL

        init(url: URL) {
            self.url = url
        }

        func numberOfPreviewItems(in controller: QLPreviewController) -> Int {
            1
        }

        func previewController(_ controller: QLPreviewController, previewItemAt index: Int) -> QLPreviewItem {
            url as NSURL
        }
    }
}

private struct LivePhotoPlayerView: UIViewControllerRepresentable {
    let url: URL

    func makeUIViewController(context: Context) -> AVPlayerViewController {
        let controller = AVPlayerViewController()
        controller.player = AVPlayer(url: url)
        controller.player?.play()
        return controller
    }

    func updateUIViewController(_ uiViewController: AVPlayerViewController, context: Context) {
        if uiViewController.player?.currentItem?.asset as? AVURLAsset == nil {
            uiViewController.player = AVPlayer(url: url)
        }
    }
}

private struct PhotoLibraryPicker: UIViewControllerRepresentable {
    let onPicked: (Data, String?) -> Void
    let onError: (String) -> Void

    func makeUIViewController(context: Context) -> PHPickerViewController {
        var config = PHPickerConfiguration(photoLibrary: .shared())
        config.filter = .any(of: [.images, .videos])
        config.selectionLimit = 1
        let picker = PHPickerViewController(configuration: config)
        picker.delegate = context.coordinator
        return picker
    }

    func updateUIViewController(_ uiViewController: PHPickerViewController, context: Context) {
    }

    func makeCoordinator() -> Coordinator {
        Coordinator(parent: self)
    }

    final class Coordinator: NSObject, PHPickerViewControllerDelegate {
        private let parent: PhotoLibraryPicker

        init(parent: PhotoLibraryPicker) {
            self.parent = parent
        }

        func picker(_ picker: PHPickerViewController, didFinishPicking results: [PHPickerResult]) {
            guard let result = results.first else {
                parent.onError("未选择照片")
                return
            }

            let provider = result.itemProvider
            let targetType = preferredTypeIdentifier(from: provider)

            provider.loadDataRepresentation(forTypeIdentifier: targetType) { data, error in
                DispatchQueue.main.async {
                    if let error {
                        self.parent.onError("选择照片失败: \(error.localizedDescription)")
                        return
                    }
                    guard let data else {
                        self.parent.onError("无法读取照片数据")
                        return
                    }
                    self.parent.onPicked(data, provider.suggestedName)
                }
            }
        }

        private func preferredTypeIdentifier(from provider: NSItemProvider) -> String {
            if provider.hasItemConformingToTypeIdentifier(UTType.image.identifier) {
                return UTType.image.identifier
            }
            if provider.hasItemConformingToTypeIdentifier(UTType.movie.identifier) {
                return UTType.movie.identifier
            }
            return provider.registeredTypeIdentifiers.first ?? UTType.data.identifier
        }
    }
}

private struct DocumentFilePicker: UIViewControllerRepresentable {
    let onPicked: (URL) -> Void
    let onError: (String) -> Void

    func makeUIViewController(context: Context) -> UIDocumentPickerViewController {
        let picker = UIDocumentPickerViewController(forOpeningContentTypes: [.data], asCopy: true)
        picker.delegate = context.coordinator
        picker.allowsMultipleSelection = false
        return picker
    }

    func updateUIViewController(_ uiViewController: UIDocumentPickerViewController, context: Context) {
    }

    func makeCoordinator() -> Coordinator {
        Coordinator(parent: self)
    }

    final class Coordinator: NSObject, UIDocumentPickerDelegate {
        private let parent: DocumentFilePicker

        init(parent: DocumentFilePicker) {
            self.parent = parent
        }

        func documentPicker(_ controller: UIDocumentPickerViewController, didPickDocumentsAt urls: [URL]) {
            guard let url = urls.first else {
                parent.onError("未选择文件")
                return
            }
            parent.onPicked(url)
        }

        func documentPickerWasCancelled(_ controller: UIDocumentPickerViewController) {
            parent.onError("已取消文件选择")
        }
    }
}

