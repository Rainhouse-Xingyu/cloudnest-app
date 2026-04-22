import Foundation
import Combine
import CryptoKit
import UIKit
import QuickLookThumbnailing

enum FilesSortMode: String, CaseIterable {
    case nameAsc
    case updatedDesc
    case sizeDesc
}

/// Files 页面状态与交互逻辑
@MainActor
final class FilesViewModel: ObservableObject {
    @Published var title: String = "我的文件"
    @Published var folders: [FileItemDTO] = []
    @Published var files: [FileItemDTO] = []
    @Published var treeNodes: [FileTreeNode] = []
    @Published var currentPath: [FilePathNode] = [FilePathNode(id: 0, name: "Root")]
    @Published var isLoading: Bool = false
    @Published var isTreeLoading: Bool = false
    @Published var currentParentId: Int64 = 0
    @Published var sortMode: FilesSortMode = .updatedDesc
    @Published var searchKeyword: String = ""
    @Published var filterFileType: String = ""
    @Published var filterStartDate: Date?
    @Published var filterEndDate: Date?
    @Published var errorMessage: String? = nil
    @Published var downloadMessage: String? = nil
    @Published var previewDocument: FilePreviewDocument?
    @Published var imagePreviewItem: ImagePreviewItem?
    @Published var mediaPlaybackItem: MediaPlaybackItem?
    @Published var livePhotoDisplayItem: LivePhotoDisplayItem?
    @Published var activityItems: [URL] = []
    @Published var shareItems: [ShareItemDTO] = []
    @Published var recycleBinItems: [FileItemDTO] = []
    @Published var latestCreatedShareLink: String?
    @Published private(set) var imageThumbnails: [Int64: UIImage] = [:]
    @Published var isUnifiedSearching: Bool = false
    @Published var unifiedSearchErrorMessage: String?
    @Published var unifiedFilenameResults: [FileItemDTO] = []
    @Published var unifiedNLResults: [AISearchItemDTO] = []

    // 上传状态
    @Published var isUploading: Bool = false
    @Published var uploadProgress: Double = 0.0

    @Published private(set) var livePhotoIDs: Set<Int64> = []

    private var livePhotoCache: [Int64: LivePhotoLookupItemDTO?] = [:]

    private let service: FilesServiceProtocol
    private let smartService: SmartServiceProtocol

    init(service: FilesServiceProtocol = FilesService(), smartService: SmartServiceProtocol = SmartService()) {
        self.service = service
        self.smartService = smartService
    }

    func onAppear() {
        Task {
            await loadData()
            await loadTree()
        }
    }
    
    func loadData(parentId: Int64? = nil) async {
        let pid = parentId ?? currentParentId
        isLoading = true
        errorMessage = nil
        do {
            let allItems: [FileItemDTO]
            if hasSearchOrFilter {
                allItems = try await service.searchFiles(
                    parentId: pid,
                    name: searchKeyword.trimmingCharacters(in: .whitespacesAndNewlines),
                    fileType: filterFileType.trimmingCharacters(in: .whitespacesAndNewlines).lowercased(),
                    startTime: formattedDate(filterStartDate),
                    endTime: formattedDate(filterEndDate),
                    page: 1,
                    limit: 200
                )
            } else {
                allItems = try await service.loadFiles(parentId: pid)
            }
            self.folders = sortedItems(allItems.filter { $0.isFolder == 1 })
            self.files = sortedItems(allItems.filter { $0.isFolder == 0 })
            self.currentParentId = pid
            rebuildPathIfNeeded(targetParentId: pid)

            await refreshLivePhotoFlags(for: self.files)
            await refreshImageThumbnails(for: self.files)
        } catch {
            self.errorMessage = "加载失败: \(error.localizedDescription)"
        }
        isLoading = false
    }

    var hasSearchOrFilter: Bool {
        let keyword = !searchKeyword.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        let type = !filterFileType.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        return keyword || type || filterStartDate != nil || filterEndDate != nil
    }

    func applySearchAndFilter(keyword: String, fileType: String, startDate: Date?, endDate: Date?) async {
        searchKeyword = keyword
        filterFileType = fileType
        filterStartDate = startDate
        filterEndDate = endDate
        await loadData(parentId: currentParentId)
    }

    func clearSearchAndFilter() async {
        searchKeyword = ""
        filterFileType = ""
        filterStartDate = nil
        filterEndDate = nil
        await loadData(parentId: currentParentId)
    }

    func runUnifiedSearch(query: String) async {
        let keyword = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !keyword.isEmpty else {
            unifiedSearchErrorMessage = "请输入搜索内容"
            unifiedFilenameResults = []
            unifiedNLResults = []
            return
        }

        isUnifiedSearching = true
        unifiedSearchErrorMessage = nil
        unifiedFilenameResults = []
        unifiedNLResults = []

        var partialErrors: [String] = []

        do {
            unifiedFilenameResults = try await service.searchFiles(
                parentId: currentParentId,
                name: keyword,
                fileType: "",
                startTime: nil,
                endTime: nil,
                page: 1,
                limit: 50
            )
        } catch {
            partialErrors.append("文件名搜索失败")
        }

        do {
            let nlResponse = try await smartService.nlSearch(
                AINLSearchRequestDTO(question: keyword, page: 1, limit: 50)
            )
            unifiedNLResults = nlResponse.items
        } catch {
            partialErrors.append("智能搜索失败")
        }

        if !partialErrors.isEmpty {
            unifiedSearchErrorMessage = partialErrors.joined(separator: "，")
        }

        isUnifiedSearching = false
    }

    func openPreviewFromSearchResult(fileId: Int64, filename: String, fileType: String, size: Int64) async {
        errorMessage = nil
        do {
            let fileURL = try await service.downloadPreview(fileId: fileId, filename: filename)
            if isImageFile(type: fileType, filename: filename) {
                imagePreviewItem = ImagePreviewItem(imageURL: fileURL, displayName: filename)
            } else if isVideoFile(type: fileType, filename: filename) || isAudioFile(type: fileType, filename: filename) {
                mediaPlaybackItem = MediaPlaybackItem(
                    mediaURL: fileURL,
                    displayName: filename,
                    isAudio: isAudioFile(type: fileType, filename: filename)
                )
            } else {
                previewDocument = FilePreviewDocument(url: fileURL, displayName: filename)
            }
        } catch {
            errorMessage = "预览失败: \(error.localizedDescription)"
        }
    }

    func loadTree() async {
        isTreeLoading = true
        defer { isTreeLoading = false }

        do {
            treeNodes = try await service.loadDirectoryTree()
            rebuildPathIfNeeded(targetParentId: currentParentId)
        } catch {
            self.errorMessage = "目录树加载失败: \(error.localizedDescription)"
        }
    }

    func enterFolder(_ folder: FileItemDTO) async {
        currentPath.append(FilePathNode(id: folder.id, name: folder.filename))
        await loadData(parentId: folder.id)
    }

    func enterFolderFromTree(folderId: Int64) async {
        rebuildPathIfNeeded(targetParentId: folderId)
        await loadData(parentId: folderId)
    }

    func navigateToPath(_ index: Int) async {
        guard index >= 0, index < currentPath.count else {
            return
        }

        let target = currentPath[index]
        currentPath = Array(currentPath.prefix(index + 1))
        await loadData(parentId: target.id)
    }

    func download(items: [FileItemDTO]) async {
        guard !items.isEmpty else {
            errorMessage = "请先选择要下载的文件或目录"
            return
        }

        errorMessage = nil
        downloadMessage = nil

        var outputFiles: [String] = []
        do {
            for item in items {
                let fileURL = try await service.downloadFile(
                    fileId: item.id,
                    filename: item.filename,
                    isFolder: item.isFolder == 1
                )
                outputFiles.append(fileURL.lastPathComponent)
            }

            if outputFiles.count == 1, let only = outputFiles.first {
                downloadMessage = "下载完成：\(only)"
            } else {
                downloadMessage = "下载完成，共 \(outputFiles.count) 个文件"
            }
        } catch {
            errorMessage = "下载失败: \(error.localizedDescription)"
        }
    }

    func openPreview(for item: FileItemDTO) async {
        errorMessage = nil
        do {
            let fileURL = try await service.downloadPreview(fileId: item.id, filename: item.filename)
            if isImageFile(item) {
                imagePreviewItem = ImagePreviewItem(imageURL: fileURL, displayName: item.filename)
            } else if isVideoFile(item) || isAudioFile(item) {
                mediaPlaybackItem = MediaPlaybackItem(
                    mediaURL: fileURL,
                    displayName: item.filename,
                    isAudio: isAudioFile(item)
                )
            } else {
                previewDocument = FilePreviewDocument(url: fileURL, displayName: item.filename)
            }
        } catch {
            errorMessage = "预览失败: \(error.localizedDescription)"
        }
    }

    func isLivePhoto(_ fileId: Int64) -> Bool {
        livePhotoIDs.contains(fileId)
    }

    func playLivePhoto(for item: FileItemDTO) async {
        guard let cached = livePhotoCache[item.id], let info = cached else {
            return
        }

        errorMessage = nil
        do {
            let imageURL = try await service.downloadPreview(
                fileId: info.imageFileId,
                filename: info.imageFile.filename
            )
            let videoURL = try await service.downloadPreview(
                fileId: info.videoFileId,
                filename: info.videoFile.filename
            )
            livePhotoDisplayItem = LivePhotoDisplayItem(
                imageURL: imageURL,
                videoURL: videoURL,
                displayName: item.filename
            )
        } catch {
            errorMessage = "实况加载失败: \(error.localizedDescription)"
        }
    }

    func uploadFile(url: URL) async {
        guard url.startAccessingSecurityScopedResource() else {
            self.errorMessage = "无法读取选择的文件权限"
            return
        }
        defer { url.stopAccessingSecurityScopedResource() }

        isUploading = true
        uploadProgress = 0.0
        errorMessage = nil

        do {
            let attributes = try FileManager.default.attributesOfItem(atPath: url.path)
            let fileSize = attributes[.size] as? Int64 ?? 0
            let filename = url.lastPathComponent
            
            // 后续可以在设置中配置这里的分片大小，目前默认设为 5MB
            let chunkSize = 5 * 1024 * 1024
            let totalChunks = Int(ceil(Double(fileSize) / Double(chunkSize)))
            
            let initReq = UploadInitRequestDTO(
                parentId: currentParentId,
                filename: filename,
                totalSize: fileSize,
                chunkSize: chunkSize,
                totalChunks: totalChunks
            )
            
            let initRes = try await service.initUpload(req: initReq)
            let uploadId = initRes.uploadId
            
            let fileHandle = try FileHandle(forReadingFrom: url)
            defer { try? fileHandle.close() }
            
            for chunkIndex in 0..<totalChunks {
                try fileHandle.seek(toOffset: UInt64(chunkIndex * chunkSize))
                let chunkData = try fileHandle.read(upToCount: chunkSize) ?? Data()
                
                let md5 = Insecure.MD5.hash(data: chunkData).map { String(format: "%02hhx", $0) }.joined()
                
                try await service.uploadChunk(
                    uploadId: uploadId,
                    chunkIndex: chunkIndex,
                    chunkMd5: md5,
                    chunkData: chunkData
                )
                
                self.uploadProgress = Double(chunkIndex + 1) / Double(totalChunks)
            }
            
            let completeReq = UploadCompleteRequestDTO(
                uploadId: uploadId,
                parentId: currentParentId
            )
            try await service.completeUpload(req: completeReq)
            
            // 上传完成后重新加载当前目录
            await loadData(parentId: currentParentId)
        } catch {
            self.errorMessage = "上传失败: \(error.localizedDescription)"
        }
        
        isUploading = false
    }

    func uploadPhotoData(_ data: Data, filename: String) async {
        let totalSize = Int64(data.count)
        guard totalSize > 0 else {
            self.errorMessage = "上传失败: 图片数据为空"
            return
        }

        isUploading = true
        uploadProgress = 0.0
        errorMessage = nil

        do {
            try await uploadDataInChunks(data, filename: filename, progressBase: 0.0, progressScale: 1.0)
            await loadData(parentId: currentParentId)
        } catch {
            self.errorMessage = "上传失败: \(error.localizedDescription)"
        }

        isUploading = false
    }

    func uploadLivePhotoPair(
        imageData: Data,
        imageFilename: String,
        videoData: Data,
        videoFilename: String
    ) async {
        guard !imageData.isEmpty, !videoData.isEmpty else {
            errorMessage = "上传失败: Live Photo 资源不完整"
            return
        }

        isUploading = true
        uploadProgress = 0.0
        errorMessage = nil

        do {
            // 先上传图片后上传视频，再触发后端 auto-bind，确保 live_photo 表生成绑定关系。
            try await uploadDataInChunks(imageData, filename: imageFilename, progressBase: 0.0, progressScale: 0.5)
            try await uploadDataInChunks(videoData, filename: videoFilename, progressBase: 0.5, progressScale: 0.5)
            try await service.autoBindLivePhotos(parentId: currentParentId, fileIds: [])
            await loadData(parentId: currentParentId)
        } catch {
            errorMessage = "上传 Live Photo 失败: \(error.localizedDescription)"
        }

        isUploading = false
    }

    private func uploadDataInChunks(
        _ data: Data,
        filename: String,
        progressBase: Double,
        progressScale: Double
    ) async throws {
        let totalSize = Int64(data.count)
        guard totalSize > 0 else {
            throw NSError(domain: "Upload", code: -1, userInfo: [NSLocalizedDescriptionKey: "文件数据为空"])
        }

        let chunkSize = 5 * 1024 * 1024
        let totalChunks = Int(ceil(Double(totalSize) / Double(chunkSize)))

        let initReq = UploadInitRequestDTO(
            parentId: currentParentId,
            filename: filename,
            totalSize: totalSize,
            chunkSize: chunkSize,
            totalChunks: totalChunks
        )
        let initRes = try await service.initUpload(req: initReq)
        let uploadId = initRes.uploadId

        for chunkIndex in 0..<totalChunks {
            let start = chunkIndex * chunkSize
            let end = min(start + chunkSize, data.count)
            let chunkData = data.subdata(in: start..<end)
            let md5 = Insecure.MD5.hash(data: chunkData).map { String(format: "%02hhx", $0) }.joined()

            try await service.uploadChunk(
                uploadId: uploadId,
                chunkIndex: chunkIndex,
                chunkMd5: md5,
                chunkData: chunkData
            )

            let currentRatio = Double(chunkIndex + 1) / Double(totalChunks)
            uploadProgress = progressBase + (currentRatio * progressScale)
        }

        let completeReq = UploadCompleteRequestDTO(
            uploadId: uploadId,
            parentId: currentParentId
        )
        try await service.completeUpload(req: completeReq)
    }

    func thumbnail(for fileId: Int64) -> UIImage? {
        imageThumbnails[fileId]
    }

    func createFolder(name: String) async {
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else {
            errorMessage = "文件夹名称不能为空"
            return
        }

        errorMessage = nil
        do {
            try await service.createFolder(parentId: currentParentId, name: trimmed)
            await loadData(parentId: currentParentId)
            await loadTree()
        } catch {
            errorMessage = "新建文件夹失败: \(error.localizedDescription)"
        }
    }

    func delete(fileIds: [Int64]) async {
        let ids = Array(Set(fileIds))
        guard !ids.isEmpty else {
            errorMessage = "请先选择要删除的项目"
            return
        }

        errorMessage = nil
        do {
            let deleted = try await service.batchDelete(fileIds: ids)
            if deleted.isEmpty {
                downloadMessage = "删除完成，但没有条目被删除"
            } else {
                downloadMessage = "已删除 \(deleted.count) 个项目"
            }
            await loadData(parentId: currentParentId)
            await loadTree()
        } catch {
            errorMessage = "删除失败: \(error.localizedDescription)"
        }
    }

    func move(fileIds: [Int64], targetParentId: Int64) async {
        let ids = Array(Set(fileIds))
        guard !ids.isEmpty else {
            errorMessage = "请先选择要移动的项目"
            return
        }

        errorMessage = nil
        do {
            try await service.move(fileIds: ids, targetParentId: targetParentId)
            downloadMessage = "移动完成"
            await loadData(parentId: currentParentId)
            await loadTree()
        } catch {
            errorMessage = "移动失败: \(error.localizedDescription)"
        }
    }

    func copy(fileIds: [Int64], targetParentId: Int64) async {
        let ids = Array(Set(fileIds))
        guard !ids.isEmpty else {
            errorMessage = "请先选择要复制的项目"
            return
        }

        errorMessage = nil
        do {
            try await service.copy(fileIds: ids, targetParentId: targetParentId)
            downloadMessage = "复制完成"
            await loadData(parentId: currentParentId)
            await loadTree()
        } catch {
            errorMessage = "复制失败: \(error.localizedDescription)"
        }
    }

    func prepareShare(items: [FileItemDTO]) async {
        guard !items.isEmpty else {
            errorMessage = "请先选择要分享的项目"
            return
        }

        errorMessage = nil
        do {
            var urls: [URL] = []
            for item in items {
                let fileURL = try await service.downloadFile(
                    fileId: item.id,
                    filename: item.filename,
                    isFolder: item.isFolder == 1
                )
                urls.append(fileURL)
            }
            activityItems = urls
        } catch {
            errorMessage = "准备分享失败: \(error.localizedDescription)"
        }
    }

    func createShareForSelection(items: [FileItemDTO], expireHours: Int? = nil) async {
        guard !items.isEmpty else {
            errorMessage = "请先选择要分享的项目"
            return
        }

        errorMessage = nil
        do {
            var links: [String] = []
            for item in items {
                let res = try await service.createShare(fileId: item.id, expireHours: expireHours)
                links.append(res.accessURL)
            }

            if let first = links.first {
                latestCreatedShareLink = first
            }
            downloadMessage = "已创建 \(links.count) 个分享链接"
            await loadShares()
        } catch {
            errorMessage = "创建分享失败: \(error.localizedDescription)"
        }
    }

    func loadShares() async {
        errorMessage = nil
        do {
            shareItems = try await service.listShares()
        } catch {
            errorMessage = "加载分享列表失败: \(error.localizedDescription)"
        }
    }

    func revokeShare(code: String) async {
        errorMessage = nil
        do {
            try await service.revokeShare(code: code)
            shareItems.removeAll { $0.shareCode == code }
        } catch {
            errorMessage = "撤销分享失败: \(error.localizedDescription)"
        }
    }

    func loadRecycleBin() async {
        errorMessage = nil
        do {
            recycleBinItems = try await service.listRecycleBin()
        } catch {
            errorMessage = "加载回收站失败: \(error.localizedDescription)"
        }
    }

    func restoreFromRecycleBin(fileId: Int64) async {
        errorMessage = nil
        do {
            try await service.restoreFromRecycleBin(fileId: fileId)
            recycleBinItems.removeAll { $0.id == fileId }
            await loadData(parentId: currentParentId)
            await loadTree()
        } catch {
            errorMessage = "恢复失败: \(error.localizedDescription)"
        }
    }

    func permanentDeleteFromRecycleBin(fileId: Int64) async {
        errorMessage = nil
        do {
            try await service.permanentDelete(fileId: fileId)
            recycleBinItems.removeAll { $0.id == fileId }
        } catch {
            errorMessage = "彻底删除失败: \(error.localizedDescription)"
        }
    }

    func clearShareItems() {
        activityItems = []
    }

    func updateSortMode(_ mode: FilesSortMode) {
        sortMode = mode
        folders = sortedItems(folders)
        files = sortedItems(files)
    }

    private func rebuildPathIfNeeded(targetParentId: Int64) {
        if targetParentId == 0 {
            currentPath = [FilePathNode(id: 0, name: "Root")]
            return
        }

        if let path = findPath(targetId: targetParentId, nodes: treeNodes) {
            currentPath = [FilePathNode(id: 0, name: "Root")] + path
        }
    }

    private func findPath(targetId: Int64, nodes: [FileTreeNode]) -> [FilePathNode]? {
        for node in nodes {
            if node.id == targetId {
                return [FilePathNode(id: node.id, name: node.name)]
            }

            if let sub = findPath(targetId: targetId, nodes: node.children) {
                return [FilePathNode(id: node.id, name: node.name)] + sub
            }
        }

        return nil
    }

    private func refreshLivePhotoFlags(for files: [FileItemDTO]) async {
        livePhotoIDs = []
        for file in files {
            do {
                if let info = try await service.getLivePhoto(byFileId: file.id) {
                    livePhotoCache[file.id] = info
                    livePhotoIDs.insert(file.id)
                } else {
                    livePhotoCache[file.id] = nil
                }
            } catch {
                // 查询失败时不阻断文件列表展示。
                livePhotoCache[file.id] = nil
            }
        }
    }

    private func refreshImageThumbnails(for files: [FileItemDTO]) async {
        var updated = imageThumbnails
        let thumbnailSize = CGSize(width: 256, height: 256)
        
        for file in files {
            guard (isImageFile(file) || isDocumentFile(file)), updated[file.id] == nil else { continue }
            do {
                if isImageFile(file) {
                    let data = try await service.downloadThumbnail(fileId: file.id)
                    if let image = UIImage(data: data) {
                        updated[file.id] = image
                    }
                } else if isDocumentFile(file) {
                    let localURL = try await service.downloadPreview(fileId: file.id, filename: file.filename)
                    
                    let request = QLThumbnailGenerator.Request(fileAt: localURL, size: thumbnailSize, scale: UIScreen.main.scale, representationTypes: .all)
                    do {
                        let rep = try await QLThumbnailGenerator.shared.generateBestRepresentation(for: request)
                        updated[file.id] = rep.uiImage
                    } catch {
                        if let image = UIImage(contentsOfFile: localURL.path) {
                            updated[file.id] = image
                        }
                    }
                }
            } catch {
                // 缩略图加载失败不影响主流程。
            }
        }
        imageThumbnails = updated
    }

    private func isDocumentFile(_ file: FileItemDTO) -> Bool {
        let lowerType = file.fileType.lowercased()
        let lowerName = file.filename.lowercased()
        if lowerType.contains("pdf") || lowerType.contains("docx") || lowerType.contains("xlsx") || lowerType.contains("pptx") || lowerType.contains("rtf") { return true }
        if lowerName.hasSuffix(".pdf") || lowerName.hasSuffix(".doc") || lowerName.hasSuffix(".docx") || lowerName.hasSuffix(".xls") || lowerName.hasSuffix(".xlsx") || lowerName.hasSuffix(".ppt") || lowerName.hasSuffix(".pptx") || lowerName.hasSuffix(".pages") || lowerName.hasSuffix(".numbers") || lowerName.hasSuffix(".key") || lowerName.hasSuffix(".rtf") || lowerName.hasSuffix(".epub") {
            return true
        }
        return false
    }

    private func isImageFile(_ file: FileItemDTO) -> Bool {
        isImageFile(type: file.fileType, filename: file.filename)
    }

    private func isVideoFile(_ file: FileItemDTO) -> Bool {
        isVideoFile(type: file.fileType, filename: file.filename)
    }

    private func isAudioFile(_ file: FileItemDTO) -> Bool {
        isAudioFile(type: file.fileType, filename: file.filename)
    }

    private func isImageFile(type: String, filename: String) -> Bool {
        let lowerType = type.lowercased()
        let lowerName = filename.lowercased()
        if lowerType.contains("image") || lowerType.contains("jpg") || lowerType.contains("jpeg") || lowerType.contains("png") || lowerType.contains("heic") || lowerType.contains("gif") || lowerType.contains("webp") {
            return true
        }
        return lowerName.hasSuffix(".jpg") || lowerName.hasSuffix(".jpeg") || lowerName.hasSuffix(".png") || lowerName.hasSuffix(".heic") || lowerName.hasSuffix(".gif") || lowerName.hasSuffix(".webp")
    }

    private func isVideoFile(type: String, filename: String) -> Bool {
        let lowerType = type.lowercased()
        let lowerName = filename.lowercased()
        if lowerType.contains("video") || lowerType.contains("mp4") || lowerType.contains("mov") || lowerType.contains("m4v") {
            return true
        }
        return lowerName.hasSuffix(".mp4") || lowerName.hasSuffix(".mov") || lowerName.hasSuffix(".m4v") || lowerName.hasSuffix(".avi")
    }

    private func isAudioFile(type: String, filename: String) -> Bool {
        let lowerType = type.lowercased()
        let lowerName = filename.lowercased()
        if lowerType.contains("audio") || lowerType.contains("mp3") || lowerType.contains("m4a") || lowerType.contains("aac") || lowerType.contains("wav") {
            return true
        }
        return lowerName.hasSuffix(".mp3") || lowerName.hasSuffix(".m4a") || lowerName.hasSuffix(".aac") || lowerName.hasSuffix(".wav") || lowerName.hasSuffix(".flac")
    }

    private func sortedItems(_ items: [FileItemDTO]) -> [FileItemDTO] {
        switch sortMode {
        case .nameAsc:
            return items.sorted { $0.filename.localizedCompare($1.filename) == .orderedAscending }
        case .updatedDesc:
            return items.sorted { $0.updateTime > $1.updateTime }
        case .sizeDesc:
            return items.sorted { $0.size > $1.size }
        }
    }

    private func formattedDate(_ value: Date?) -> String? {
        guard let value else { return nil }
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "zh_CN")
        formatter.timeZone = TimeZone.current
        formatter.dateFormat = "yyyy-MM-dd HH:mm:ss"
        return formatter.string(from: value)
    }
}


extension FilesViewModel {
    func resolveLivePhotoPreview(for id: Int64) async throws -> (URL, URL)? {
        guard let cached = livePhotoCache[id], let info = cached else {
            return nil
        }
        let iURL = try await service.downloadPreview(fileId: info.imageFileId, filename: info.imageFile.filename)
        let vURL = try await service.downloadPreview(fileId: info.videoFileId, filename: info.videoFile.filename)
        return (iURL, vURL)
    }
    
    func resolveImagePreview(for id: Int64, filename: String) async throws -> URL {
        return try await service.downloadPreview(fileId: id, filename: filename)
    }
}
