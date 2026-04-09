import Foundation

/// Settings 模块数据模型（示例）
struct SettingsModel: Identifiable {
    let id: UUID
    var title: String

    init(id: UUID = UUID(), title: String = "Settings Item") {
        self.id = id
        self.title = title
    }
}
