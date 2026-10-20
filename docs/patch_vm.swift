import Foundation

let path = "cloudnest_ios/cloudnest_ios/Features/Files/ViewModels/FilesViewModel.swift"
var source = try! String(contentsOfFile: path)

source = source.replacingOccurrences(of: """
    // 上传状态
    @Published var isUploading: Bool = false
    @Published var uploadProgress: Double = 0.0

""", with: "")

source = source.replacingOccurrences(of: """
        isUploading = true
        uploadProgress = 0.0
        errorMessage = nil
""", with: """
        errorMessage = nil
""")

source = source.replacingOccurrences(of: """
        isUploading = false
""", with: "")


let search1 = """
    func uploadPhotoData(_ data: Data, filename: String) async {
        let totalSize = Int64(data.count)
        guard totalSize > 0 else {
            self.errorMessage = "上传失败: 图片数据为空"
            return
        }

        errorMessage = nil

        do {
            try await uploadDataInChunks(data, filename: filename, progressBase: 0.0, progressScale: 1.0)
            await loadData(parentId: currentParentId)
        } catch {
            self.errorMessage = "上传失败: \\(error.localizedDescription)"
        }
    }
"""

let repl1 = """
    func uploadPhotoData(_ data: Data, filename: String) async {
        let totalSize = Int64(data.count)
        guard totalSize > 0 else {
            self.errorMessage = "上传失败: 图片数据为空"
            return
        }

        errorMessage = nil
        let taskId = TransferManager.shared.addTask(name: filename, type: .upload)

        do {
            try await uploadDataInChunks(data, filename: filename, taskId: taskId, progressBase: 0.0, progressScale: 1.0)
            TransferManager.shared.completeTask(id: taskId)
            await loadData(parentId: currentParentId)
        } catch {
            TransferManager.shared.failTask(id: taskId, error: error.localizedDescription)
            self.errorMessage = "上传失败: \\(error.localizedDescription)"
        }
    }
"""
source = source.replacingOccurrences(of: search1, with: repl1)

let search2 = """
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

        errorMessage = nil

        do {
            // 先上传图片后上传视频，再触发后端 auto-bind，确保 live_photo 表生成绑定关系。
            try await uploadDataInChunks(imageData, filename: imageFilename, progressBase: 0.0, progressScale: 0.5)
            try await uploadDataInChunks(videoData, filename: videoFilename, progressBase: 0.5, progressScale: 0.5)
            try await service.autoBindLivePhotos(parentId: currentParentId, fileIds: [])
            await loadData(parentId: currentParentId)
        } catch {
            errorMessage = "上传 Live Photo 失败: \\(error.localizedDescription)"
        }
    }
"""

let repl2 = """
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

        errorMessage = nil
        let taskId = TransferManager.shared.addTask(name: imageFilename + " (Live)", type: .upload)

        do {
            // 先上传图片后上传视频，再触发后端 auto-bind，确保 live_photo 表生成绑定关系。
            try await uploadDataInChunks(imageData, filename: imageFilename, taskId: taskId, progressBase: 0.0, progressScale: 0.5)
            try await uploadDataInChunks(videoData, filename: videoFilename, taskId: taskId, progressBase: 0.5, progressScale: 0.5)
            try await service.autoBindLivePhotos(parentId: currentParentId, fileIds: [])
            TransferManager.shared.completeTask(id: taskId)
            await loadData(parentId: currentParentId)
        } catch {
            TransferManager.shared.failTask(id: taskId, error: error.localizedDescription)
            errorMessage = "上传 Live Photo 失败: \\(error.localizedDescription)"
        }
    }
"""
source = source.replacingOccurrences(of: search2, with: repl2)

let search3 = """
    private func uploadDataInChunks(
        _ data: Data,
        filename: String,
        progressBase: Double,
        progressScale: Double
    ) async throws {
"""

let repl3 = """
    private func uploadDataInChunks(
        _ data: Data,
        filename: String,
        taskId: UUID,
        progressBase: Double,
        progressScale: Double
    ) async throws {
"""
source = source.replacingOccurrences(of: search3, with: repl3)

let search4 = """
            let currentRatio = Double(chunkIndex + 1) / Double(totalChunks)
            uploadProgress = progressBase + (currentRatio * progressScale)
"""

let repl4 = """
            let currentRatio = Double(chunkIndex + 1) / Double(totalChunks)
            TransferManager.shared.updateTaskProgress(id: taskId, progress: progressBase + (currentRatio * progressScale))
"""
source = source.replacingOccurrences(of: search4, with: repl4)

try! source.write(toFile: path, atomically: true, encoding: .utf8)
