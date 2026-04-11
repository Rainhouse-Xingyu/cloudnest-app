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
