import SwiftUI
import UniformTypeIdentifiers
import QuickLook
import AVKit
import PhotosUI
import Photos
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
    @State private var showUploadSourceDialog: Bool = false
    @State private var showCreateFolderAlert: Bool = false
    @State private var newFolderName: String = ""
    @State private var pendingOperation: PendingFileOperation?
    @State private var tempFilterFileType: String = ""
    @State private var tempStartDate: Date = Date()
    @State private var tempEndDate: Date = Date()
    @State private var useStartDate: Bool = false
    @State private var useEndDate: Bool = false
    @State private var activeSheet: FilesActiveSheet?
    @State private var expandedTreeNodes: Set<Int64> = []
    @State private var showUnifiedSearchOverlay: Bool = false
    @State private var unifiedSearchText: String = ""
    @FocusState private var isUnifiedSearchFocused: Bool
    @AppStorage("files.layout.style") private var layoutStyleRaw: String = FilesLayoutStyle.icon.rawValue

    private let folderColumns = [
        GridItem(.flexible(), spacing: 14),
        GridItem(.flexible(), spacing: 14)
    ]

    var body: some View {
        styledPageView
    }

    private var contentLayerView: some View {
        mainScrollView
            .overlay(alignment: .bottom) {
                selectionBottomBar
            }
            .overlay {
                uploadingOverlay
            }
            .overlay {
                unifiedSearchOverlay
            }
    }

    private var presentationLayerView: some View {
        contentLayerView
            .alert("确认删除", isPresented: $showDeleteConfirm) {
                Button("取消", role: .cancel) { }
                Button("删除", role: .destructive) {
                    let ids = Array(selectedItems)
                    Task {
                        await viewModel.delete(fileIds: ids)
                        withAnimation {
                            selectedItems.removeAll()
                            isSelectionMode = false
                        }
                    }
                }
            } message: {
                Text("确定要删除选中的 \(selectedItems.count) 个项目吗？")
            }
            .alert("新建文件夹", isPresented: $showCreateFolderAlert) {
                TextField("请输入文件夹名称", text: $newFolderName)
                Button("取消", role: .cancel) {
                    newFolderName = ""
                }
                Button("创建") {
                    let name = newFolderName
                    newFolderName = ""
                    Task {
                        await viewModel.createFolder(name: name)
                    }
                }
            } message: {
                Text("将在当前目录创建新文件夹")
            }
            .confirmationDialog("选择上传来源", isPresented: $showUploadSourceDialog, titleVisibility: .visible) {
                Button("照片 App") {
                    activeSheet = .photoPicker
                }
                Button("文件 App") {
                    activeSheet = .filePicker
                }
                Button("取消", role: .cancel) { }
            }
            .sheet(item: $activeSheet, onDismiss: {
                if !viewModel.activityItems.isEmpty {
                    viewModel.clearShareItems()
                }
            }) { sheet in
                activeSheetView(sheet)
            }
            .alert("下载结果", isPresented: downloadAlertBinding) {
                Button("确定", role: .cancel) {
                    viewModel.downloadMessage = nil
                }
            } message: {
                Text(viewModel.downloadMessage ?? "")
            }
    }

    private var stateObservationLayerView: some View {
        presentationLayerView
            .onChange(of: viewModel.previewDocument) { doc in
                guard let doc, activeSheet == nil else { return }
                activeSheet = .quickLook(doc)
                viewModel.previewDocument = nil
            }
            .onChange(of: viewModel.imagePreviewItem) { item in
                guard let item, activeSheet == nil else { return }
                activeSheet = .imagePreview(item)
                viewModel.imagePreviewItem = nil
            }
            .onChange(of: viewModel.mediaPlaybackItem) { item in
                guard let item, activeSheet == nil else { return }
                activeSheet = .mediaPlayback(item)
                viewModel.mediaPlaybackItem = nil
            }
            .onChange(of: viewModel.livePhotoDisplayItem) { item in
                guard let item, activeSheet == nil else { return }
                activeSheet = .livePhoto(item)
                viewModel.livePhotoDisplayItem = nil
            }
    }

    private var styledPageView: some View {
        stateObservationLayerView
            .liquidGlassPage()
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                filesToolbar
            }
            .onAppear(perform: handleOnAppear)
    }

    private var mainScrollView: some View {
        ScrollView(showsIndicators: false) {
            VStack(spacing: 16) {
                headerBar
                breadcrumbBar
                directoryTreeSection
                personalSection
                mainContentSection
            }
            .padding(.horizontal, 16)
            .padding(.top, 10)
            .padding(.bottom, 24)
        }
    }

    @ViewBuilder
    private var mainContentSection: some View {
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
            listContentSection
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

    @ViewBuilder
    private var listContentSection: some View {
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
    }

    @ViewBuilder
    private var selectionBottomBar: some View {
        if !selectedItems.isEmpty {
            bottomActionBar
        }
    }

    @ViewBuilder
    private var uploadingOverlay: some View {
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

    private var downloadAlertBinding: Binding<Bool> {
        Binding(
            get: { viewModel.downloadMessage != nil },
            set: { newValue in
                if !newValue {
                    viewModel.downloadMessage = nil
                }
            }
        )
    }

    private func handleOnAppear() {
        viewModel.onAppear()
        tempFilterFileType = viewModel.filterFileType
        if let start = viewModel.filterStartDate {
            useStartDate = true
            tempStartDate = start
        }
        if let end = viewModel.filterEndDate {
            useEndDate = true
            tempEndDate = end
        }
    }

    private func resetFilterInputs() {
        tempFilterFileType = ""
        useStartDate = false
        useEndDate = false
        tempStartDate = Date()
        tempEndDate = Date()
    }

    private func applyCurrentSearchAndFilter() async {
        await viewModel.applySearchAndFilter(
            keyword: viewModel.searchKeyword,
            fileType: tempFilterFileType,
            startDate: useStartDate ? tempStartDate : nil,
            endDate: useEndDate ? tempEndDate : nil
        )
    }

    @ToolbarContentBuilder
    private var filesToolbar: some ToolbarContent {
        ToolbarItem(placement: .principal) {
            Text("文件")
                .font(.appTitle)
                .fontWeight(.semibold)
        }

        ToolbarItemGroup(placement: .topBarTrailing) {
            Button {
                withAnimation(.easeInOut(duration: 0.2)) {
                    showUnifiedSearchOverlay = true
                }
                unifiedSearchText = viewModel.searchKeyword
                isUnifiedSearchFocused = true
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
                showCreateFolderAlert = true
            } label: {
                Image(systemName: "folder.badge.plus")
            }

            Menu {
                Button(action: {
                    Task { await viewModel.loadData() }
                }) {
                    Label("刷新", systemImage: "arrow.clockwise")
                }
                Menu {
                    Button {
                        viewModel.updateSortMode(.updatedDesc)
                    } label: {
                        if viewModel.sortMode == .updatedDesc {
                            Label("按更新时间", systemImage: "checkmark")
                        } else {
                            Text("按更新时间")
                        }
                    }

                    Button {
                        viewModel.updateSortMode(.nameAsc)
                    } label: {
                        if viewModel.sortMode == .nameAsc {
                            Label("按名称", systemImage: "checkmark")
                        } else {
                            Text("按名称")
                        }
                    }

                    Button {
                        viewModel.updateSortMode(.sizeDesc)
                    } label: {
                        if viewModel.sortMode == .sizeDesc {
                            Label("按大小", systemImage: "checkmark")
                        } else {
                            Text("按大小")
                        }
                    }
                } label: {
                    Label("排序", systemImage: "arrow.up.arrow.down")
                }
                Button(action: {
                    activeSheet = .filter
                }) {
                    Label("筛选", systemImage: "line.3.horizontal.decrease.circle")
                }
                Button(action: {
                    Task { await viewModel.loadRecycleBin() }
                    activeSheet = .recycleBin
                }) {
                    Label("回收站", systemImage: "trash")
                }
                Button(action: {
                    Task { await viewModel.loadShares() }
                    activeSheet = .shareManage
                }) {
                    Label("分享管理", systemImage: "link")
                }
            } label: {
                Image(systemName: "ellipsis.circle")
            }
        }
    }

    @ViewBuilder
    private var unifiedSearchOverlay: some View {
        if showUnifiedSearchOverlay {
            ZStack(alignment: .top) {
                Color(.systemBackground)
                    .ignoresSafeArea()

                VStack(spacing: 12) {
                    HStack {
                        Button {
                            withAnimation(.easeInOut(duration: 0.2)) {
                                showUnifiedSearchOverlay = false
                            }
                            isUnifiedSearchFocused = false
                        } label: {
                            Label("返回", systemImage: "chevron.left")
                                .font(.headline)
                        }
                        .buttonStyle(.bordered)

                        Spacer()
                    }

                    HStack(spacing: 8) {
                        Image(systemName: "magnifyingglass")
                            .foregroundStyle(.secondary)

                        TextField("搜索文件名，或输入自然语言描述", text: $unifiedSearchText)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled(true)
                            .focused($isUnifiedSearchFocused)
                            .onSubmit {
                                Task {
                                    await runUnifiedSearch()
                                }
                            }

                        if !unifiedSearchText.isEmpty {
                            Button {
                                unifiedSearchText = ""
                            } label: {
                                Image(systemName: "xmark.circle.fill")
                                    .foregroundStyle(.secondary)
                            }
                        }

                        Button("搜索") {
                            Task {
                                await runUnifiedSearch()
                            }
                        }
                        .buttonStyle(.borderedProminent)
                        .disabled(viewModel.isUnifiedSearching)
                    }
                    .padding(.horizontal, 12)
                    .padding(.vertical, 10)
                    .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 14, style: .continuous))

                    if viewModel.isUnifiedSearching {
                        ProgressView("搜索中...")
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }

                    if let message = viewModel.unifiedSearchErrorMessage {
                        Text(message)
                            .font(.footnote)
                            .foregroundStyle(.red)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }

                    ScrollView(showsIndicators: false) {
                        VStack(alignment: .leading, spacing: 12) {
                            if !viewModel.unifiedFilenameResults.isEmpty {
                                Text("文件名搜索结果")
                                    .font(.headline)

                                VStack(spacing: 8) {
                                    ForEach(viewModel.unifiedFilenameResults) { item in
                                        unifiedFilenameResultRow(item)
                                    }
                                }
                            }

                            if !viewModel.unifiedNLResults.isEmpty {
                                Text("智能搜索结果")
                                    .font(.headline)
                                    .padding(.top, viewModel.unifiedFilenameResults.isEmpty ? 0 : 6)

                                VStack(spacing: 8) {
                                    ForEach(viewModel.unifiedNLResults) { item in
                                        unifiedNLResultRow(item)
                                    }
                                }
                            }

                            if !viewModel.isUnifiedSearching,
                               viewModel.unifiedFilenameResults.isEmpty,
                               viewModel.unifiedNLResults.isEmpty,
                               !unifiedSearchText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                                Text("未搜索到结果")
                                    .font(.footnote)
                                    .foregroundStyle(.secondary)
                                    .frame(maxWidth: .infinity, alignment: .leading)
                                    .padding(.top, 10)
                            }
                        }
                        .padding(.vertical, 8)
                    }
                }
                .padding(.horizontal, 16)
                .padding(.top, 14)
                .padding(.bottom, 20)
                .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
            }
            .transition(.opacity)
            .zIndex(20)
        }
    }

    private func runUnifiedSearch() async {
        await viewModel.runUnifiedSearch(query: unifiedSearchText)
    }

    private func unifiedFilenameResultRow(_ item: FileItemDTO) -> some View {
        Button {
            Task {
                if item.isFolder == 1 {
                    withAnimation(.easeInOut(duration: 0.2)) {
                        showUnifiedSearchOverlay = false
                    }
                    await viewModel.enterFolder(item)
                } else {
                    withAnimation(.easeInOut(duration: 0.2)) {
                        showUnifiedSearchOverlay = false
                    }
                    await viewModel.openPreview(for: item)
                }
            }
        } label: {
            HStack(spacing: 10) {
                Image(systemName: item.isFolder == 1 ? "folder" : getFileIcon(type: item.fileType))
                    .foregroundStyle(item.isFolder == 1 ? Color.blue : Color.orange)
                    .frame(width: 22)

                VStack(alignment: .leading, spacing: 3) {
                    Text(item.filename)
                        .font(.subheadline.weight(.semibold))
                        .lineLimit(1)
                    Text(item.isFolder == 1 ? "文件夹" : "\(formatFileSize(item.size))")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Spacer()
            }
            .padding(10)
            .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        }
        .buttonStyle(.plain)
    }

    private func unifiedNLResultRow(_ item: AISearchItemDTO) -> some View {
        Button {
            Task {
                withAnimation(.easeInOut(duration: 0.2)) {
                    showUnifiedSearchOverlay = false
                }
                await viewModel.openPreviewFromSearchResult(
                    fileId: item.id,
                    filename: item.filename,
                    fileType: item.fileType,
                    size: item.size
                )
            }
        } label: {
            VStack(alignment: .leading, spacing: 6) {
                HStack(spacing: 10) {
                    Image(systemName: getFileIcon(type: item.fileType))
                        .foregroundStyle(Color.orange)
                    Text(item.filename)
                        .font(.subheadline.weight(.semibold))
                        .lineLimit(1)
                    Spacer()
                }

                if !item.ocrSnippet.isEmpty {
                    Text(item.ocrSnippet)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(2)
                } else if !item.description.isEmpty {
                    Text(item.description)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(2)
                }
            }
            .padding(10)
            .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        }
        .buttonStyle(.plain)
    }

    @ViewBuilder
    private func activeSheetView(_ sheet: FilesActiveSheet) -> some View {
        switch sheet {
        case .photoPicker:
            photoPickerSheet

        case .filePicker:
            filePickerSheet

        case .targetFolder:
            targetFolderSheet

        case .shareActivity:
            shareActivitySheet

        case .shareManage:
            shareManageSheet

        case .recycleBin:
            recycleBinSheet

        case .filter:
            filterSheet

        case .quickLook(let doc):
            FileQuickLookPreview(url: doc.url)

        case .imagePreview(let item):
            NativeImagePreviewSheet(item: item)

        case .mediaPlayback(let item):
            NativeMediaPlayerView(item: item)

        case .livePhoto(let item):
            LivePhotoSheetView(item: item)
        }
    }

    private var photoPickerSheet: some View {
        PhotoLibraryPicker(
            onPicked: { data, originalName in
                let filename = buildPhotoFilename(originalName: originalName, data: data)
                activeSheet = nil
                Task {
                    await viewModel.uploadPhotoData(data, filename: filename)
                }
            },
            onPickedLivePhoto: { payload in
                activeSheet = nil
                Task {
                    await viewModel.uploadLivePhotoPair(
                        imageData: payload.imageData,
                        imageFilename: payload.imageFilename,
                        videoData: payload.videoData,
                        videoFilename: payload.videoFilename
                    )
                }
            },
            onError: { message in
                activeSheet = nil
                viewModel.errorMessage = message
            }
        )
        .ignoresSafeArea()
    }

    private var filePickerSheet: some View {
        DocumentFilePicker(
            onPicked: { url in
                activeSheet = nil
                Task {
                    await viewModel.uploadFile(url: url)
                }
            },
            onError: { message in
                activeSheet = nil
                viewModel.errorMessage = message
            }
        )
        .ignoresSafeArea()
    }

    private var targetFolderSheet: some View {
        FolderTargetPickerView(
            treeNodes: viewModel.treeNodes,
            currentParentId: viewModel.currentParentId,
            operation: pendingOperation,
            onCancel: {
                activeSheet = nil
                pendingOperation = nil
            },
            onConfirm: { targetParentId in
                let op = pendingOperation
                let ids = Array(selectedItems)
                activeSheet = nil
                pendingOperation = nil

                Task {
                    if op == .move {
                        await viewModel.move(fileIds: ids, targetParentId: targetParentId)
                    } else if op == .copy {
                        await viewModel.copy(fileIds: ids, targetParentId: targetParentId)
                    }
                    withAnimation {
                        selectedItems.removeAll()
                        isSelectionMode = false
                    }
                }
            }
        )
    }

    private var shareActivitySheet: some View {
        ShareSheet(activityItems: viewModel.activityItems)
    }

    private var shareManageSheet: some View {
        ShareManageSheetView(
            items: viewModel.shareItems,
            latestCreatedLink: viewModel.latestCreatedShareLink,
            onRefresh: {
                Task { await viewModel.loadShares() }
            },
            onRevoke: { code in
                Task { await viewModel.revokeShare(code: code) }
            }
        )
    }

    private var recycleBinSheet: some View {
        RecycleBinSheetView(
            items: viewModel.recycleBinItems,
            onRefresh: {
                Task { await viewModel.loadRecycleBin() }
            },
            onRestore: { id in
                Task { await viewModel.restoreFromRecycleBin(fileId: id) }
            },
            onPermanentDelete: { id in
                Task { await viewModel.permanentDeleteFromRecycleBin(fileId: id) }
            }
        )
    }

    private var filterSheet: some View {
        FilesFilterSheet(
            fileType: $tempFilterFileType,
            startDate: $tempStartDate,
            endDate: $tempEndDate,
            useStartDate: $useStartDate,
            useEndDate: $useEndDate,
            onReset: resetFilterInputs,
            onApply: {
                activeSheet = nil
                Task {
                    await applyCurrentSearchAndFilter()
                }
            }
        )
    }

    private var searchBarSection: some View {
        VStack(spacing: 8) {
            HStack(spacing: 8) {
                Image(systemName: "magnifyingglass")
                    .foregroundStyle(.secondary)

                TextField("按文件名搜索", text: $viewModel.searchKeyword)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled(true)
                    .onSubmit {
                        Task {
                            await applyCurrentSearchAndFilter()
                        }
                    }

                if !viewModel.searchKeyword.isEmpty {
                    Button {
                        viewModel.searchKeyword = ""
                        Task {
                            await applyCurrentSearchAndFilter()
                        }
                    } label: {
                        Image(systemName: "xmark.circle.fill")
                            .foregroundStyle(.secondary)
                    }
                }
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 10)
            .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 12, style: .continuous))

            HStack(spacing: 10) {
                Button {
                    activeSheet = .filter
                } label: {
                    Label(viewModel.hasSearchOrFilter ? "筛选中" : "筛选", systemImage: "line.3.horizontal.decrease.circle")
                }
                .buttonStyle(.bordered)

                if viewModel.hasSearchOrFilter {
                    Button {
                        tempFilterFileType = ""
                        useStartDate = false
                        useEndDate = false
                        tempStartDate = Date()
                        tempEndDate = Date()
                        Task { await viewModel.clearSearchAndFilter() }
                    } label: {
                        Label("清空条件", systemImage: "xmark")
                    }
                    .buttonStyle(.bordered)
                }

                Spacer()
            }
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
                    if viewModel.isLivePhoto(item.id) {
                        await viewModel.playLivePhoto(for: item)
                    } else {
                        await viewModel.openPreview(for: item)
                    }
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
                Task {
                    if viewModel.isLivePhoto(item.id) {
                        await viewModel.playLivePhoto(for: item)
                    } else {
                        await viewModel.openPreview(for: item)
                    }
                }
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
                pendingOperation = .move
                activeSheet = .targetFolder
            }
            Spacer()
            actionButton(title: "复制", icon: "doc.on.doc") {
                pendingOperation = .copy
                activeSheet = .targetFolder
            }
            Spacer()
            actionButton(title: "分享", icon: "square.and.arrow.up") {
                let allItems = viewModel.folders + viewModel.files
                let toShare = allItems.filter { selectedItems.contains($0.id) }
                Task {
                    await viewModel.createShareForSelection(items: toShare)
                    await viewModel.loadShares()
                    activeSheet = .shareManage
                }
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

private enum PendingFileOperation {
    case move
    case copy
}

private enum FilesActiveSheet: Identifiable, Equatable {
    case photoPicker
    case filePicker
    case targetFolder
    case shareActivity
    case shareManage
    case recycleBin
    case filter
    case quickLook(FilePreviewDocument)
    case imagePreview(ImagePreviewItem)
    case mediaPlayback(MediaPlaybackItem)
    case livePhoto(LivePhotoDisplayItem)

    var id: String {
        switch self {
        case .photoPicker:
            return "photoPicker"
        case .filePicker:
            return "filePicker"
        case .targetFolder:
            return "targetFolder"
        case .shareActivity:
            return "shareActivity"
        case .shareManage:
            return "shareManage"
        case .recycleBin:
            return "recycleBin"
        case .filter:
            return "filter"
        case .quickLook(let doc):
            return "quickLook_\(doc.id.uuidString)"
        case .imagePreview(let item):
            return "imagePreview_\(item.id.uuidString)"
        case .mediaPlayback(let item):
            return "mediaPlayback_\(item.id.uuidString)"
        case .livePhoto(let item):
            return "livePhoto_\(item.id.uuidString)"
        }
    }

    static func == (lhs: FilesActiveSheet, rhs: FilesActiveSheet) -> Bool {
        lhs.id == rhs.id
    }
}

private struct FolderTargetPickerView: View {
    let treeNodes: [FileTreeNode]
    let currentParentId: Int64
    let operation: PendingFileOperation?
    let onCancel: () -> Void
    let onConfirm: (Int64) -> Void

    @State private var selectedTargetId: Int64 = 0
    @State private var expandedNodeIDs: Set<Int64> = []

    var body: some View {
        NavigationStack {
            List {
                Section("目标目录") {
                    folderRow(id: 0, name: "Root", level: 0)
                    ForEach(treeNodes) { node in
                        treeRow(node: node, level: 1)
                    }
                }
            }
            .navigationTitle(operationTitle)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button("取消") {
                        onCancel()
                    }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button("确定") {
                        onConfirm(selectedTargetId)
                    }
                }
            }
            .onAppear {
                selectedTargetId = currentParentId
            }
        }
    }

    private var operationTitle: String {
        if operation == .move { return "移动到" }
        if operation == .copy { return "复制到" }
        return "选择目录"
    }

    private func treeRow(node: FileTreeNode, level: Int) -> AnyView {
        AnyView(
            VStack(alignment: .leading, spacing: 0) {
                HStack(spacing: 8) {
                    if !node.children.isEmpty {
                        Button {
                            if expandedNodeIDs.contains(node.id) {
                                expandedNodeIDs.remove(node.id)
                            } else {
                                expandedNodeIDs.insert(node.id)
                            }
                        } label: {
                            Image(systemName: expandedNodeIDs.contains(node.id) ? "chevron.down" : "chevron.right")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                        .buttonStyle(.plain)
                    } else {
                        Color.clear.frame(width: 16, height: 16)
                    }

                    folderRow(id: node.id, name: node.name, level: level)
                }

                if expandedNodeIDs.contains(node.id) {
                    ForEach(node.children) { child in
                        treeRow(node: child, level: level + 1)
                    }
                }
            }
        )
    }

    private func folderRow(id: Int64, name: String, level: Int) -> some View {
        Button {
            selectedTargetId = id
        } label: {
            HStack(spacing: 8) {
                Image(systemName: "folder")
                    .foregroundStyle(.blue)
                Text(name)
                    .foregroundStyle(.primary)
                Spacer()
                if selectedTargetId == id {
                    Image(systemName: "checkmark.circle.fill")
                        .foregroundStyle(.blue)
                }
            }
            .padding(.leading, CGFloat(level) * 10)
        }
        .buttonStyle(.plain)
    }
}

private struct FilesFilterSheet: View {
    @Binding var fileType: String
    @Binding var startDate: Date
    @Binding var endDate: Date
    @Binding var useStartDate: Bool
    @Binding var useEndDate: Bool

    let onReset: () -> Void
    let onApply: () -> Void

    var body: some View {
        NavigationStack {
            Form {
                Section("文件类型") {
                    TextField("例如：jpg / png / mp4 / pdf", text: $fileType)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled(true)
                    Text("后端按 file_type 精确匹配")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                }

                Section("更新时间") {
                    Toggle("启用开始时间", isOn: $useStartDate)
                    if useStartDate {
                        DatePicker("开始时间", selection: $startDate, displayedComponents: [.date, .hourAndMinute])
                    }

                    Toggle("启用结束时间", isOn: $useEndDate)
                    if useEndDate {
                        DatePicker("结束时间", selection: $endDate, displayedComponents: [.date, .hourAndMinute])
                    }
                }
            }
            .navigationTitle("筛选")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button("重置") {
                        onReset()
                    }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button("应用") {
                        onApply()
                    }
                }
            }
        }
    }
}

private struct ShareManageSheetView: View {
    let items: [ShareItemDTO]
    let latestCreatedLink: String?
    let onRefresh: () -> Void
    let onRevoke: (String) -> Void

    var body: some View {
        NavigationStack {
            List {
                if let latestCreatedLink, !latestCreatedLink.isEmpty {
                    Section("最近创建") {
                        VStack(alignment: .leading, spacing: 8) {
                            Text(latestCreatedLink)
                                .font(.footnote)
                                .foregroundStyle(.secondary)
                                .textSelection(.enabled)

                            Button("复制链接") {
                                UIPasteboard.general.string = latestCreatedLink
                            }
                            .buttonStyle(.borderedProminent)
                        }
                        .padding(.vertical, 4)
                    }
                }

                Section("分享列表") {
                    if items.isEmpty {
                        Text("暂无分享链接")
                            .foregroundStyle(.secondary)
                    } else {
                        ForEach(items) { item in
                            VStack(alignment: .leading, spacing: 6) {
                                Text(item.shareCode)
                                    .font(.headline)
                                Text("创建时间: \(item.createTime)")
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                                if let expire = item.expireTime {
                                    Text("过期时间: \(expire)")
                                        .font(.caption)
                                        .foregroundStyle(.secondary)
                                } else {
                                    Text("永不过期")
                                        .font(.caption)
                                        .foregroundStyle(.secondary)
                                }
                            }
                            .swipeActions(edge: .trailing) {
                                Button(role: .destructive) {
                                    onRevoke(item.shareCode)
                                } label: {
                                    Label("撤销", systemImage: "trash")
                                }
                            }
                        }
                    }
                }
            }
            .navigationTitle("分享管理")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        onRefresh()
                    } label: {
                        Image(systemName: "arrow.clockwise")
                    }
                }
            }
        }
    }
}

private struct RecycleBinSheetView: View {
    let items: [FileItemDTO]
    let onRefresh: () -> Void
    let onRestore: (Int64) -> Void
    let onPermanentDelete: (Int64) -> Void

    var body: some View {
        NavigationStack {
            List {
                if items.isEmpty {
                    Text("回收站为空")
                        .foregroundStyle(.secondary)
                } else {
                    ForEach(items) { item in
                        VStack(alignment: .leading, spacing: 6) {
                            Text(item.filename)
                                .font(.headline)
                            Text("删除时间: \(item.updateTime)")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                        .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                            Button(role: .destructive) {
                                onPermanentDelete(item.id)
                            } label: {
                                Label("彻底删除", systemImage: "trash.fill")
                            }

                            Button {
                                onRestore(item.id)
                            } label: {
                                Label("恢复", systemImage: "arrow.uturn.backward")
                            }
                            .tint(.blue)
                        }
                    }
                }
            }
            .navigationTitle("回收站")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        onRefresh()
                    } label: {
                        Image(systemName: "arrow.clockwise")
                    }
                }
            }
        }
    }
}

private struct ShareSheet: UIViewControllerRepresentable {
    let activityItems: [Any]

    func makeUIViewController(context: Context) -> UIActivityViewController {
        UIActivityViewController(activityItems: activityItems, applicationActivities: nil)
    }

    func updateUIViewController(_ uiViewController: UIActivityViewController, context: Context) {
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

private struct NativeImagePreviewSheet: View {
    let item: ImagePreviewItem

    var body: some View {
        ZStack {
            Color.black.ignoresSafeArea()

            VStack(spacing: 8) {
                HStack {
                    Text(item.displayName)
                        .font(.headline)
                        .foregroundStyle(.white)
                        .lineLimit(1)
                    Spacer()
                }
                .padding(.horizontal, 16)
                .padding(.top, 12)

                GeometryReader { geo in
                    if let image = UIImage(contentsOfFile: item.imageURL.path) {
                        Image(uiImage: image)
                            .resizable()
                            .scaledToFit()
                            .frame(width: geo.size.width, height: geo.size.height)
                    } else {
                        VStack(spacing: 8) {
                            Image(systemName: "photo")
                                .font(.system(size: 42))
                                .foregroundStyle(.white.opacity(0.7))
                            Text("图片加载失败")
                                .foregroundStyle(.white.opacity(0.85))
                        }
                        .frame(width: geo.size.width, height: geo.size.height)
                    }
                }
            }
        }
    }
}

private struct NativeMediaPlayerView: UIViewControllerRepresentable {
    let item: MediaPlaybackItem

    func makeUIViewController(context: Context) -> AVPlayerViewController {
        let controller = AVPlayerViewController()
        controller.player = AVPlayer(url: item.mediaURL)
        controller.showsPlaybackControls = true
        controller.player?.play()
        return controller
    }

    func updateUIViewController(_ uiViewController: AVPlayerViewController, context: Context) {
        guard let current = (uiViewController.player?.currentItem?.asset as? AVURLAsset)?.url else {
            uiViewController.player = AVPlayer(url: item.mediaURL)
            if !item.isAudio {
                uiViewController.player?.play()
            }
            return
        }

        if current != item.mediaURL {
            uiViewController.player = AVPlayer(url: item.mediaURL)
            if !item.isAudio {
                uiViewController.player?.play()
            }
        }
    }
}

private struct LivePhotoSheetView: View {
    let item: LivePhotoDisplayItem
    @State private var isSaving: Bool = false
    @State private var saveResultMessage: String?

    var body: some View {
        VStack(spacing: 12) {
            HStack {
                Text(item.displayName)
                    .font(.headline)
                    .lineLimit(1)
                Spacer()
                Button {
                    Task { await saveToPhotoLibrary() }
                } label: {
                    if isSaving {
                        ProgressView()
                            .controlSize(.small)
                    } else {
                        Label("保存", systemImage: "square.and.arrow.down")
                    }
                }
                .buttonStyle(.borderedProminent)
                .disabled(isSaving)
            }
            .padding(.horizontal, 16)
            .padding(.top, 8)

            LivePhotoDisplayView(imageURL: item.imageURL, videoURL: item.videoURL)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(Color.black.opacity(0.92))
                .clipShape(RoundedRectangle(cornerRadius: 16, style: .continuous))
                .padding(.horizontal, 12)
                .padding(.bottom, 12)
        }
        .background(Color(.systemBackground))
        .alert("保存结果", isPresented: Binding(
            get: { saveResultMessage != nil },
            set: { newValue in
                if !newValue { saveResultMessage = nil }
            }
        )) {
            Button("好的", role: .cancel) { saveResultMessage = nil }
        } message: {
            Text(saveResultMessage ?? "")
        }
    }

    private func saveToPhotoLibrary() async {
        guard !isSaving else { return }
        isSaving = true
        defer { isSaving = false }

        let status = await requestPhotoLibraryPermission()
        guard status == .authorized || status == .limited else {
            saveResultMessage = "未获得相册保存权限，请在系统设置中允许照片访问"
            return
        }

        do {
            try await performSaveLivePhoto(imageURL: item.imageURL, videoURL: item.videoURL)
            saveResultMessage = "已保存到系统相册"
        } catch {
            saveResultMessage = "保存失败: \(error.localizedDescription)"
        }
    }

    private func requestPhotoLibraryPermission() async -> PHAuthorizationStatus {
        let current = PHPhotoLibrary.authorizationStatus(for: .addOnly)
        if current == .notDetermined {
            return await withCheckedContinuation { continuation in
                PHPhotoLibrary.requestAuthorization(for: .addOnly) { status in
                    continuation.resume(returning: status)
                }
            }
        }
        return current
    }

    private func performSaveLivePhoto(imageURL: URL, videoURL: URL) async throws {
        try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Void, Error>) in
            PHPhotoLibrary.shared().performChanges {
                let request = PHAssetCreationRequest.forAsset()
                let options = PHAssetResourceCreationOptions()
                options.shouldMoveFile = false
                // 使用 Apple 官方 Live Photo 资源写入 API，分别写入静态图与配对视频。
                request.addResource(with: .photo, fileURL: imageURL, options: options)
                request.addResource(with: .pairedVideo, fileURL: videoURL, options: options)
            } completionHandler: { success, error in
                if let error {
                    continuation.resume(throwing: error)
                    return
                }
                if success {
                    continuation.resume()
                } else {
                    continuation.resume(throwing: NSError(
                        domain: "LivePhotoSave",
                        code: -1,
                        userInfo: [NSLocalizedDescriptionKey: "系统未返回具体错误"]
                    ))
                }
            }
        }
    }
}

private struct LivePhotoDisplayView: UIViewRepresentable {
    let imageURL: URL
    let videoURL: URL

    func makeUIView(context: Context) -> PHLivePhotoView {
        let view = PHLivePhotoView()
        view.contentMode = .scaleAspectFit
        context.coordinator.load(into: view, imageURL: imageURL, videoURL: videoURL)
        return view
    }

    func updateUIView(_ uiView: PHLivePhotoView, context: Context) {
        context.coordinator.load(into: uiView, imageURL: imageURL, videoURL: videoURL)
    }

    func makeCoordinator() -> Coordinator {
        Coordinator()
    }

    final class Coordinator {
        private var lastKey: String?

        func load(into view: PHLivePhotoView, imageURL: URL, videoURL: URL) {
            let key = imageURL.path + "|" + videoURL.path
            guard lastKey != key else { return }
            lastKey = key

            PHLivePhoto.request(
                withResourceFileURLs: [imageURL, videoURL],
                placeholderImage: nil,
                targetSize: CGSize(width: 1200, height: 1200),
                contentMode: .aspectFit
            ) { livePhoto, _ in
                DispatchQueue.main.async {
                    view.livePhoto = livePhoto
                    if livePhoto != nil {
                        view.startPlayback(with: .full)
                    }
                }
            }
        }
    }
}

private struct PhotoLibraryPicker: UIViewControllerRepresentable {
    struct LivePhotoPayload {
        let imageData: Data
        let imageFilename: String
        let videoData: Data
        let videoFilename: String
    }

    let onPicked: (Data, String?) -> Void
    let onPickedLivePhoto: (LivePhotoPayload) -> Void
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

            Task {
                do {
                    if let payload = try await self.extractLivePhotoPayload(from: result) {
                        await MainActor.run {
                            self.parent.onPickedLivePhoto(payload)
                        }
                        return
                    }

                    let provider = result.itemProvider
                    let targetType = preferredTypeIdentifier(from: provider)
                    let data = try await loadDataRepresentation(provider: provider, typeIdentifier: targetType)
                    await MainActor.run {
                        self.parent.onPicked(data, provider.suggestedName)
                    }
                } catch {
                    await MainActor.run {
                        self.parent.onError("选择照片失败: \(error.localizedDescription)")
                    }
                }
            }
        }

        private func extractLivePhotoPayload(from result: PHPickerResult) async throws -> LivePhotoPayload? {
            if let byAsset = try await extractLivePhotoPayloadFromAsset(result) {
                return byAsset
            }
            return try await extractLivePhotoPayloadFromProvider(result.itemProvider)
        }

        private func extractLivePhotoPayloadFromAsset(_ result: PHPickerResult) async throws -> LivePhotoPayload? {
            guard let assetIdentifier = result.assetIdentifier else {
                return nil
            }

            let status = await requestPhotoReadPermissionIfNeeded()
            guard status == .authorized || status == .limited else {
                return nil
            }

            let assets = PHAsset.fetchAssets(withLocalIdentifiers: [assetIdentifier], options: nil)
            guard let asset = assets.firstObject else {
                return nil
            }

            let resources = PHAssetResource.assetResources(for: asset)
            guard let imageResource = resources.first(where: { $0.type == .photo || $0.type == .fullSizePhoto }),
                  let videoResource = resources.first(where: { $0.type == .pairedVideo }) else {
                return nil
            }

            async let imageTask = loadAssetResourceData(imageResource)
            async let videoTask = loadAssetResourceData(videoResource)
            let (imageData, videoData) = try await (imageTask, videoTask)

            guard !imageData.isEmpty, !videoData.isEmpty else {
                return nil
            }

            return LivePhotoPayload(
                imageData: imageData,
                imageFilename: imageResource.originalFilename,
                videoData: videoData,
                videoFilename: videoResource.originalFilename
            )
        }

        private func extractLivePhotoPayloadFromProvider(_ provider: NSItemProvider) async throws -> LivePhotoPayload? {
            guard provider.hasItemConformingToTypeIdentifier(UTType.image.identifier),
                  provider.hasItemConformingToTypeIdentifier(UTType.movie.identifier) else {
                return nil
            }

            async let imageDataTask = loadDataRepresentation(
                provider: provider,
                typeIdentifier: UTType.image.identifier
            )
            async let videoDataTask = loadDataRepresentation(
                provider: provider,
                typeIdentifier: UTType.movie.identifier
            )
            let (imageData, videoData) = try await (imageDataTask, videoDataTask)

            guard !imageData.isEmpty, !videoData.isEmpty else {
                return nil
            }

            let baseName = provider.suggestedName ?? "LivePhoto_\(Int(Date().timeIntervalSince1970))"
            // auto-bind 依赖同主干名 + 图片/视频扩展名，这里强制视频扩展名为 mov。
            let imageFilename = buildFilename(baseName: baseName, ext: "heic", forceReplaceExtension: false)
            let videoFilename = buildFilename(baseName: baseName, ext: "mov", forceReplaceExtension: true)

            return LivePhotoPayload(
                imageData: imageData,
                imageFilename: imageFilename,
                videoData: videoData,
                videoFilename: videoFilename
            )
        }

        private func requestPhotoReadPermissionIfNeeded() async -> PHAuthorizationStatus {
            let current = PHPhotoLibrary.authorizationStatus(for: .readWrite)
            if current == .notDetermined {
                return await withCheckedContinuation { continuation in
                    PHPhotoLibrary.requestAuthorization(for: .readWrite) { status in
                        continuation.resume(returning: status)
                    }
                }
            }
            return current
        }

        private func loadAssetResourceData(_ resource: PHAssetResource) async throws -> Data {
            try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Data, Error>) in
                var receivedData = Data()
                let options = PHAssetResourceRequestOptions()
                options.isNetworkAccessAllowed = true

                PHAssetResourceManager.default().requestData(for: resource, options: options) { chunk in
                    receivedData.append(chunk)
                } completionHandler: { error in
                    if let error {
                        continuation.resume(throwing: error)
                        return
                    }
                    continuation.resume(returning: receivedData)
                }
            }
        }

        private func loadDataRepresentation(provider: NSItemProvider, typeIdentifier: String) async throws -> Data {
            try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Data, Error>) in
                provider.loadDataRepresentation(forTypeIdentifier: typeIdentifier) { data, error in
                    if let error {
                        continuation.resume(throwing: error)
                        return
                    }
                    guard let data else {
                        continuation.resume(throwing: NSError(
                            domain: "PhotoPicker",
                            code: -1,
                            userInfo: [NSLocalizedDescriptionKey: "无法读取照片数据"]
                        ))
                        return
                    }
                    continuation.resume(returning: data)
                }
            }
        }

        private func buildFilename(baseName: String, ext: String, forceReplaceExtension: Bool) -> String {
            let trimmed = baseName.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !trimmed.isEmpty else {
                return "file.\(ext)"
            }

            let stem = (trimmed as NSString).deletingPathExtension
            let currentExt = (trimmed as NSString).pathExtension

            if forceReplaceExtension {
                return (stem.isEmpty ? trimmed : stem) + ".\(ext)"
            }

            if currentExt.isEmpty {
                return trimmed + ".\(ext)"
            }
            return trimmed
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

