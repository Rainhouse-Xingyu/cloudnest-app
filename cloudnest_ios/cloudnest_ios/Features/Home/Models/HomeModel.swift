import Foundation

/// 首页照片条目
struct HomePhotoItem: Identifiable {
    let id: UUID
    let title: String
    let capturedAt: String

    init(id: UUID = UUID(), title: String, capturedAt: String) {
        self.id = id
        self.title = title
        self.capturedAt = capturedAt
    }
}

/// 首页文件条目
struct HomeFileItem: Identifiable {
    let id: UUID
    let name: String
    let sizeText: String
    let updatedAt: String

    init(id: UUID = UUID(), name: String, sizeText: String, updatedAt: String) {
        self.id = id
        self.name = name
        self.sizeText = sizeText
        self.updatedAt = updatedAt
    }
}

/// 首页容量信息
struct HomeCapacityInfo {
    let usedSpace: Int64
    let totalSpace: Int64
    let usedText: String
    let totalText: String
    let percentage: Double
    
    init(usedSpace: Int64, totalSpace: Int64, usedText: String, totalText: String) {
        self.usedSpace = usedSpace
        self.totalSpace = totalSpace
        self.usedText = usedText
        self.totalText = totalText
        self.percentage = totalSpace > 0 ? Double(usedSpace) / Double(totalSpace) : 0
    }
}
