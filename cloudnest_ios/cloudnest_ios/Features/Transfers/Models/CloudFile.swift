import Foundation

/// 云端文件模型（照片/视频统一入口）
struct CloudFile: Identifiable, Equatable {
    let id: Int64
    let filename: String
    let fileType: String
    let isLivePhoto: Bool
    let isVideo: Bool

    init(
        id: Int64,
        filename: String,
        fileType: String,
        isLivePhoto: Bool? = nil,
        isVideo: Bool? = nil
    ) {
        self.id = id
        self.filename = filename
        self.fileType = fileType
        let normalized = fileType.lowercased()
        self.isLivePhoto = isLivePhoto ?? normalized.contains("livephoto")
        self.isVideo = isVideo ?? normalized.contains("video")
    }
}
