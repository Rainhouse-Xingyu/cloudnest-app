import Foundation

struct TransferIdentity: Codable, Equatable, Sendable {
    let server: String
    let userID: Int64
}

struct UploadSource: Codable, Sendable {
    let size: Int64
    let chunkSize: Int
    let chunks: [String]
    let sha256: String
}

struct TransferRecord: Codable, Identifiable, Sendable {
    enum Direction: String, Codable {
        case upload, download, versionDownload, versionUpload
        var isUpload: Bool { self == .upload || self == .versionUpload }
        var isDownload: Bool { self == .download || self == .versionDownload }
    }
    enum Phase: String, Codable {
        case queued, running, pausing, paused, cancelling, cancelled, failed, completed
        var isActive: Bool { self == .running || self == .pausing || self == .cancelling }
    }

    let id: UUID
    let createdAt: Date
    let identity: TransferIdentity
    let direction: Direction
    let name: String
    let bookmark: Data
    let parentID: Int64
    let fileID: Int64?
    let isArchive: Bool
    var revision: Int64 = 0
    var phase: Phase = .queued
    var totalBytes: Int64
    var confirmedBytes: Int64 = 0
    var source: UploadSource?
    var uploadID: String?
    var epoch: Int64 = 0
    var etag: String?
    var sha256: String?
    var downloadedChunks: [String] = []
    var resultFileID: Int64?
    var resultName: String?
    var cancelRequested = false
    var error: String?
    // Preview tasks are transient and never written to the durable journal.
    var previewID: UUID?
    // versionDownload is deliberately a new enum value: older clients must
    // refuse this journal rather than ignore these fields and fetch current bytes.
    var fileVersion: Int?
    var versionSize: Int64?
    // A separate kind makes older clients refuse rather than publish this as
    // an ordinary file. The confirmed base never advances on retry.
    var baseVersion: Int?
    var versionComment: String?
    var versionTargetName: String?
    var publishedVersion: Int?
}

enum TransferError: LocalizedError, Equatable {
    case sourceChanged, invalidCheckpoint, integrity, alreadyRunning, invalidSource, invalidDownload, localStorage, destinationExists
    case previewTooLarge, previewCapacity, previewStorage, legacyPreview

    var errorDescription: String? {
        switch self {
        case .sourceChanged: "源文件已变化，请取消此任务并重新选择文件。"
        case .invalidCheckpoint: "传输检查点无法验证，请重新创建任务。"
        case .integrity: "文件完整性校验失败，请检查后重试。"
        case .alreadyRunning: "另一份 CloudNest 正在管理传输，请使用已打开的应用窗口。"
        case .invalidSource: "请选择普通文件，上传文件最大为 1 TiB。"
        case .invalidDownload: "服务器的下载身份或分段响应无效。"
        case .destinationExists: "目标中已有同名文件，将使用新的文件名。"
        case .localStorage: "无法读写本地传输文件，请检查磁盘空间及目录访问权限。"
        case .previewTooLarge: "文件超过预览缓存限制或已变大，请先下载后打开。"
        case .previewCapacity: "预览缓存已满，请关闭其他预览后重试。"
        case .previewStorage: "无法验证预览缓存目录，请检查文件访问权限。"
        case .legacyPreview: "旧版预览文件和记录已保留，不再继续或取消。需要文件时，请从文件列表重新下载到其他位置。"
        }
    }
}
