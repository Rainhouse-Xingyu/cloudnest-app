import Foundation

/// Profile 模块数据模型（示例）
struct ProfileModel: Identifiable {
    let id: UUID
    var title: String

    init(id: UUID = UUID(), title: String = "Profile Item") {
        self.id = id
        self.title = title
    }
}
