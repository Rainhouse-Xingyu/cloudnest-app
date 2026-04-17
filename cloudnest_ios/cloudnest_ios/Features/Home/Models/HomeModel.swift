import Foundation

/// 首页照片条目
struct HomePhotoItem: Identifiable {
    let id: Int64
    let title: String
    let capturedAt: String
    let imageFileID: Int64
    let videoFileID: Int64

    init(id: Int64, title: String, capturedAt: String, imageFileID: Int64, videoFileID: Int64) {
        self.id = id
        self.title = title
        self.capturedAt = capturedAt
        self.imageFileID = imageFileID
        self.videoFileID = videoFileID
    }
}

/// 首页文件条目
struct HomeFileItem: Identifiable {
    let id: Int64
    let name: String
    let sizeText: String
    let updatedAt: String

    init(id: Int64, name: String, sizeText: String, updatedAt: String) {
        self.id = id
        self.name = name
        self.sizeText = sizeText
        self.updatedAt = updatedAt
    }
}

/// 首页容量信息
struct HomeCapacityInfo {
    let usedSpace: Int64
    let totalSpace: Int64?
    let usedText: String
    let totalText: String?
    let summaryText: String
    let hasQuota: Bool
    let quotaSource: String
    let percentage: Double?
    
    init(usedSpace: Int64, totalSpace: Int64?, usedText: String, totalText: String?, summaryText: String, hasQuota: Bool = false, quotaSource: String = "none", usagePercent: Double? = nil) {
        self.usedSpace = usedSpace
        self.totalSpace = totalSpace
        self.usedText = usedText
        self.totalText = totalText
        self.summaryText = summaryText
        self.hasQuota = hasQuota
        self.quotaSource = quotaSource
        if let usagePercent {
            self.percentage = min(1, max(0, usagePercent / 100))
        } else if let totalSpace, totalSpace > 0 {
            self.percentage = min(1, Double(usedSpace) / Double(totalSpace))
        } else {
            self.percentage = nil
        }
    }
}

/// 首页状态卡片信息
struct HomeStatusInfo {
    let frpDeviceTotal: Int
    let frpDeviceEnabled: Int
    let rateWindowText: String
}
