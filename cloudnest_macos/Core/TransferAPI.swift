import Foundation
import CryptoKit

struct TransferContext: Sendable {
    let identity: TransferIdentity
    let origin: ServerOrigin
    let token: String
    let api: ServerAPI
}

struct UploadStatus: Decodable, Sendable {
    let uploadID: String
    let epoch: Int64
    let status: Int
    let uploaded: [Int]?
    enum CodingKeys: String, CodingKey {
        case status
        case uploadID = "upload_id"
        case epoch = "transfer_epoch"
        case uploaded = "uploaded_chunks"
    }
}

extension ServerAPI {
    private struct InitUpload: Encodable {
        let filename: String
        let parent_id: Int64
        let total_size: Int64
        let chunk_size: Int
        let total_chunks: Int
    }
    private struct UploadID: Encodable { let upload_id: String }
    private struct UploadControl: Encodable {
        let upload_id: String
        let expected_epoch: Int64
    }
    private struct CompleteUpload: Encodable {
        let upload_id: String
        let parent_id: Int64
    }
    private struct UploadReceipt: Decodable { let item: FileItem }
    private struct ChunkReceipt: Decodable { let message: String }
    private struct CancelReceipt: Decodable { let status: Int }

    func initializeUpload(_ context: TransferContext, name: String, parentID: Int64,
                          source: UploadSource) async throws -> UploadStatus {
        let input = InitUpload(filename: name, parent_id: parentID, total_size: source.size,
                               chunk_size: source.chunkSize, total_chunks: source.chunks.count)
        return try await request(context.origin, "api/files/upload/init", token: context.token,
                                 body: JSONEncoder().encode(input), method: "POST")
    }

    func uploadProgress(_ context: TransferContext, id: String) async throws -> UploadStatus {
        try await request(context.origin, "api/files/upload/progress", token: context.token,
                          query: [URLQueryItem(name: "upload_id", value: id)], maximumBytes: 1024 * 1024)
    }

    func controlUpload(_ context: TransferContext, id: String, epoch: Int64, paused: Bool) async throws -> UploadStatus {
        let action = paused ? "pause" : "resume"
        return try await request(context.origin, "api/files/upload/\(action)", token: context.token,
                                 body: JSONEncoder().encode(UploadControl(upload_id: id, expected_epoch: epoch)), method: "POST")
    }

    func cancelUpload(_ context: TransferContext, id: String) async throws {
        let result: CancelReceipt = try await request(context.origin, "api/files/upload/cancel", token: context.token,
                                                      body: JSONEncoder().encode(UploadID(upload_id: id)), method: "POST")
        guard result.status == 3 else { throw ServerError.invalidResponse }
    }

    func uploadChunk(_ context: TransferContext, id: String, epoch: Int64, index: Int, data: Data) async throws {
        let boundary = "CloudNest-\(UUID())"
        var body = Data()
        let fields = [("upload_id", id), ("chunk_index", String(index)),
                      ("chunk_md5", TransferDisk.hex(Insecure.MD5.hash(data: data)))]
        for (name, value) in fields {
            body.append(Data("--\(boundary)\r\nContent-Disposition: form-data; name=\"\(name)\"\r\n\r\n\(value)\r\n".utf8))
        }
        body.append(Data("--\(boundary)\r\nContent-Disposition: form-data; name=\"chunk\"; filename=\"chunk.bin\"\r\nContent-Type: application/octet-stream\r\n\r\n".utf8))
        body.append(data)
        body.append(Data("\r\n--\(boundary)--\r\n".utf8))
        let _: ChunkReceipt = try await request(context.origin, "api/files/upload/chunk", token: context.token,
                                                body: body, method: "POST", headers: ["X-Upload-Epoch": String(epoch)],
                                                contentType: "multipart/form-data; boundary=\(boundary)", transfer: true)
    }

    func completeUpload(_ context: TransferContext, id: String, epoch: Int64, parentID: Int64) async throws -> FileItem {
        let response: UploadReceipt = try await request(context.origin, "api/files/upload/complete", token: context.token,
                                                        body: JSONEncoder().encode(CompleteUpload(upload_id: id, parent_id: parentID)),
                                                        method: "POST", headers: ["X-Upload-Epoch": String(epoch)], transfer: true)
        return response.item
    }
}
