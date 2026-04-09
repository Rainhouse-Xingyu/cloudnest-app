import Foundation

/// Auth 模块数据模型（示例）
struct AuthModel: Identifiable {
    let id: UUID
    var title: String

    init(id: UUID = UUID(), title: String = "Auth Item") {
        self.id = id
        self.title = title
    }
}
