import Foundation

struct PhotoQuery: Equatable, Sendable {
    let name: String
    init(name: String = "") throws {
        let value = name.trimmingCharacters(in:.whitespacesAndNewlines)
        guard value.utf8.count <= 200, value.rangeOfCharacter(from:.controlCharacters) == nil else { throw ServerError.invalidOperation }
        self.name = value
    }
}

struct PhotoPage: Sendable {
    let query: PhotoQuery
    let page: Int
    let total: Int64
    let items: [FileItem]
    var hasNext: Bool { Int64(page) * 50 < total }
}

enum PhotoContract {
    static let imageTypes: Set<String> = ["jpg","jpeg","png","gif","webp","heic","heif","tif","tiff","bmp","avif"]
    static func valid(_ file: FileItem) -> Bool {
        let ext = (file.filename as NSString).pathExtension.lowercased()
        return file.id > 0 && file.parentID >= 0 && file.isFolder == 0 && file.size >= 0 && file.currentVersion > 0
            && !file.filename.isEmpty && file.filename.utf8.count <= 1024
            && URL(fileURLWithPath:file.filename).lastPathComponent == file.filename
            && !file.filename.utf8.contains(0) && imageTypes.contains(ext)
            && (file.fileType.isEmpty || imageTypes.contains(file.fileType.lowercased()))
            && (file.userID.map { $0 > 0 } ?? true) && (file.workspaceID.map { $0 > 0 } ?? true)
    }
}

extension ServerAPI {
    func photos(_ origin: ServerOrigin, token: String, query: PhotoQuery, page: Int) async throws -> PhotoPage {
        struct Response: Decodable {
            struct Pagination: Decodable { let page: Int; let limit: Int; let total: Int64 }
            let items: [FileItem]?; let media_kind: String?; let pagination: Pagination
        }
        guard page > 0, page <= Int.max / 50 else { throw ServerError.invalidOperation }
        let response: Response = try await request(origin,"api/files/search",token:token,query:[
            URLQueryItem(name:"media_kind",value:"image"),URLQueryItem(name:"name",value:query.name),
            URLQueryItem(name:"page",value:String(page)),URLQueryItem(name:"limit",value:"50")],maximumBytes:256*1024)
        let rows = response.items ?? [], pagination = response.pagination
        guard response.media_kind == "image", pagination.page == page, pagination.limit == 50,
              pagination.total >= rows.count, rows.count <= 50, rows.allSatisfy(PhotoContract.valid),
              Set(rows.map(\.id)).count == rows.count else { throw ServerError.invalidResponse }
        return PhotoPage(query:query,page:page,total:pagination.total,items:rows)
    }
}
