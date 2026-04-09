import Foundation

/// Files 模块数据模型（示例）
struct FilesModel: Identifiable {
    let id: UUID
    var title: String

    init(id: UUID = UUID(), title: String = "Files Item") {
        self.id = id
        self.title = title
    }
}
