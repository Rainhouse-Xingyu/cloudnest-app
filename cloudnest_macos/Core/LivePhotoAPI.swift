import Foundation

struct LivePhotoResource: Decodable, Equatable, Identifiable, DownloadableFile {
    let id: Int64
    let filename: String
    let size: Int64
    let parentID: Int64
    let userID: Int64
    let fileType: String
    let currentVersion: Int
    var isDirectory: Bool { false }
    enum CodingKeys: String, CodingKey {
        case id, filename, size
        case parentID = "parent_id", userID = "user_id", fileType = "file_type", currentVersion = "current_version"
    }
    func validate(image: Bool) throws {
        let ext = (filename as NSString).pathExtension.lowercased()
        guard id > 0, size >= 0, parentID >= 0, userID > 0, currentVersion > 0,
              !filename.isEmpty, filename.utf8.count <= 1024, !filename.utf8.contains(0),
              URL(fileURLWithPath:filename).lastPathComponent == filename,
              image ? ["jpg","jpeg","heic","heif"].contains(ext) : ext == "mov" else { throw ServerError.invalidResponse }
    }
}

struct LivePhotoPair: Decodable, Equatable, Identifiable, Sendable {
    let id: Int64
    let imageFileID: Int64
    let videoFileID: Int64
    let image: LivePhotoResource
    let video: LivePhotoResource
    enum CodingKeys: String, CodingKey {
        case id
        case imageFileID = "image_file_id", videoFileID = "video_file_id"
        case image = "image_file", video = "video_file"
    }
    func validate() throws {
        guard id > 0, imageFileID == image.id, videoFileID == video.id, image.id != video.id else { throw ServerError.invalidResponse }
        try image.validate(image:true); try video.validate(image:false)
    }
}

struct LivePhotoPage: Sendable {
    let page: Int
    let total: Int64
    let items: [LivePhotoPair]
    var hasNext: Bool { Int64(page)*50 < total }
}

struct MotionCandidatePage: Sendable {
    let page: Int
    let total: Int64
    let items: [FileItem]
    var hasNext: Bool { Int64(page)*50 < total }
}

extension ServerAPI {
    func livePhotos(_ origin: ServerOrigin, token: String, page: Int) async throws -> LivePhotoPage {
        struct Response: Decodable {
            struct Pagination: Decodable { let page: Int; let limit: Int; let total: Int64 }
            let items: [LivePhotoPair]?; let pagination: Pagination
        }
        guard page > 0, page <= Int.max/50 else { throw ServerError.invalidOperation }
        let response: Response = try await request(origin,"api/live-photos",token:token,query:[URLQueryItem(name:"page",value:String(page)),URLQueryItem(name:"limit",value:"50")],maximumBytes:256*1024)
        let rows = response.items ?? [], p = response.pagination
        guard p.page == page, p.limit == 50, p.total >= rows.count, rows.count <= 50,
              Set(rows.map(\.id)).count == rows.count,
              Set(rows.flatMap { [$0.imageFileID,$0.videoFileID] }).count == rows.count*2 else { throw ServerError.invalidResponse }
        for row in rows { try row.validate() }
        return LivePhotoPage(page:page,total:p.total,items:rows)
    }
    func livePhoto(_ origin: ServerOrigin, token: String, id: Int64) async throws -> LivePhotoPair {
        struct Response: Decodable { let item: LivePhotoPair }
        guard id > 0 else { throw ServerError.invalidOperation }
        let response: Response = try await request(origin,"api/live-photos/\(id)",token:token,maximumBytes:16*1024)
        try response.item.validate()
        guard response.item.id == id else { throw ServerError.invalidResponse }
        return response.item
    }
    func bindLivePhoto(_ origin: ServerOrigin, token: String, imageID: Int64, videoID: Int64) async throws -> LivePhotoPair {
        struct Input: Encodable { let image_file_id: Int64; let video_file_id: Int64 }
        struct Response: Decodable { let item: LivePhotoPair }
        guard imageID > 0, videoID > 0, imageID != videoID else { throw ServerError.invalidOperation }
        let body = try JSONEncoder().encode(Input(image_file_id:imageID,video_file_id:videoID))
        let response: Response = try await request(origin,"api/live-photos/bind",token:token,body:body,method:"POST",maximumBytes:16*1024)
        try response.item.validate()
        guard response.item.imageFileID == imageID, response.item.videoFileID == videoID else { throw ServerError.invalidResponse }
        return response.item
    }
    func unbindLivePhoto(_ origin: ServerOrigin, token: String, id: Int64) async throws {
        struct Message: Decodable { let message: String }
        guard id > 0 else { throw ServerError.invalidOperation }
        let response: Message = try await request(origin,"api/live-photos/\(id)",token:token,method:"DELETE")
        guard response.message == "unbound" else { throw ServerError.invalidResponse }
    }
    func motionCandidates(_ origin: ServerOrigin, token: String, query: PhotoQuery, page: Int) async throws -> MotionCandidatePage {
        struct Response: Decodable {
            struct Pagination: Decodable { let page: Int; let limit: Int; let total: Int64 }
            let items: [FileItem]?; let pagination: Pagination; let media_kind: String?
        }
        guard page > 0, page <= Int.max/50 else { throw ServerError.invalidOperation }
        let result: Response = try await request(origin,"api/files/search",token:token,query:[URLQueryItem(name:"media_kind",value:"live_photo_motion"),URLQueryItem(name:"name",value:query.name),URLQueryItem(name:"page",value:String(page)),URLQueryItem(name:"limit",value:"50")],maximumBytes:256*1024)
        let rows = result.items ?? [], p = result.pagination
        guard result.media_kind == "live_photo_motion", p.page == page, p.limit == 50, p.total >= rows.count, rows.count <= 50,
              Set(rows.map(\.id)).count == rows.count,
              rows.allSatisfy({ $0.id > 0 && $0.parentID >= 0 && $0.isFolder == 0 && $0.size >= 0 && ($0.fileType.isEmpty || $0.fileType.lowercased() == "mov") && ($0.filename as NSString).pathExtension.lowercased() == "mov" && !$0.filename.isEmpty && !$0.filename.utf8.contains(0) && URL(fileURLWithPath:$0.filename).lastPathComponent == $0.filename }) else { throw ServerError.invalidResponse }
        return MotionCandidatePage(page:page,total:p.total,items:rows)
    }
}
