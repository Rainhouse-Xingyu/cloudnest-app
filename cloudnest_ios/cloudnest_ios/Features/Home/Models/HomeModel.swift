import Foundation

/// Home 模块数据模型（示例）
struct HomeModel: Identifiable {
    let id: UUID
    var title: String

    init(id: UUID = UUID(), title: String = "Home Item") {
        self.id = id
        self.title = title
    }
}
