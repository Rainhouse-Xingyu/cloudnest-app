import Foundation
import Combine
import CryptoKit

/// Files 页面状态与交互逻辑
@MainActor
final class FilesViewModel: ObservableObject {
    @Published var title: String = "我的文件"
    @Published var folders: [FileItemDTO] = []
    @Published var files: [FileItemDTO] = []
    @Published var isLoading: Bool = false
    @Published var currentParentId: Int64 = 0
    @Published var errorMessage: String? = nil

    // 上传状态
    @Published var isUploading: Bool = false
    @Published var uploadProgress: Double = 0.0

    private let service: FilesServiceProtocol

    init(service: FilesServiceProtocol = FilesService()) {
        self.service = service
    }

    func onAppear() {
        Task {
            await loadData()
        }
    }
    
    func loadData(parentId: Int64? = nil) async {
        let pid = parentId ?? currentParentId
        isLoading = true
        errorMessage = nil
        do {
            let allItems = try await service.loadFiles(parentId: pid)
            self.folders = allItems.filter { $0.isFolder == 1 }
            self.files = allItems.filter { $0.isFolder == 0 }
            self.currentParentId = pid
        } catch {
            self.errorMessage = "加载失败: \(error.localizedDescription)"
        }
        isLoading = false
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
}


