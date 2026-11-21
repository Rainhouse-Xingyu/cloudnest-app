import Foundation

// Search hits are not FileItem: the search contract does not return a version.
struct AISearchHit: Decodable, Identifiable, DownloadableFile {
    let id: Int64
    let filename: String
    let size: Int64
    let parentID: Int64
    let userID: Int64
    let snippet: String
    let matchSources: [String]
    let extractionKind: String
    let indexGeneration: Int64
    let snippetOffsetBasis: String
    let snippetStartRune: Int
    let chunkOrdinal: Int
    // The hybrid API selects only active, non-directory files.
    var isDirectory: Bool { false }

    enum CodingKeys: String, CodingKey {
        case id, filename, size, snippet
        case parentID = "parent_id", userID = "user_id", matchSources = "match_sources"
        case extractionKind = "extraction_kind", indexGeneration = "index_generation"
        case snippetOffsetBasis = "snippet_offset_basis", snippetStartRune = "snippet_start_rune"
        case chunkOrdinal = "chunk_ordinal"
    }
}

struct AISearchResponse: Decodable, Sendable {
    struct Pagination: Decodable, Sendable { let page: Int; let limit: Int; let total: Int }
    struct Retrieval: Decodable, Sendable {
        let mode: String
        let warnings: [String]
        let windowLimited: Bool
        let windowSize: Int
        enum CodingKeys: String, CodingKey {
            case mode, warnings
            case windowLimited = "window_limited", windowSize = "window_size"
        }
    }
    let items: [AISearchHit]
    let pagination: Pagination
    let retrieval: Retrieval
}

struct AIIndexItem: Decodable, Identifiable, Sendable {
    let fileID: Int64
    let filename: String
    let generation: Int64
    let status: String
    let attempts: Int
    let errorCode: String
    let sourceCurrent: Bool
    let indexedCurrent: Bool
    let chunkCount: Int
    var id: Int64 { fileID }
    enum CodingKeys: String, CodingKey {
        case filename, generation, status, attempts
        case fileID = "file_id", errorCode = "error_code", sourceCurrent = "source_current"
        case indexedCurrent = "indexed_current", chunkCount = "chunk_count"
    }
}

struct AIIndexPage: Decodable, Sendable {
    let items: [AIIndexItem]
    let nextAfterID: Int64
    let hasMore: Bool
    enum CodingKeys: String, CodingKey {
        case items
        case nextAfterID = "next_after_id", hasMore = "has_more"
    }
}

extension ServerAPI {
    func searchAI(_ origin: ServerOrigin, token: String, question: String, page: Int) async throws -> AISearchResponse {
        struct Input: Encodable { let question: String; let page: Int; let limit = 20 }
        let question = question.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !question.isEmpty, question.utf8.count <= 4096, (1...10).contains(page) else {
            throw ServerError.invalidOperation
        }
        let result: AISearchResponse = try await request(origin, "api/ai/search/nl", token: token,
            body: JSONEncoder().encode(Input(question: question, page: page)), method: "POST", maximumBytes: 1024 * 1024)
        guard result.pagination.page == page, result.pagination.limit == 20,
              (0...200).contains(result.pagination.total), (1...200).contains(result.retrieval.windowSize),
              result.items.count <= 20, Set(result.items.map(\.id)).count == result.items.count,
              result.items.allSatisfy({ $0.id > 0 && $0.size >= 0 && $0.parentID >= 0 && $0.userID > 0 &&
                  $0.indexGeneration >= 0 && $0.snippetStartRune >= 0 && $0.chunkOrdinal >= 0 }) else {
            throw ServerError.invalidResponse
        }
        return result
    }

    func indexAI(_ origin: ServerOrigin, token: String, after: Int64) async throws -> AIIndexPage {
        guard after >= 0 else { throw ServerError.invalidOperation }
        let result: AIIndexPage = try await request(origin, "api/ai/index", token: token,
            query: [URLQueryItem(name: "after_id", value: String(after)), URLQueryItem(name: "limit", value: "50")],
            maximumBytes: 256 * 1024)
        let ids = result.items.map(\.id)
        guard ids.count <= 50, ids == ids.sorted(), Set(ids).count == ids.count,
              result.items.allSatisfy({ $0.id > after && $0.generation >= 0 && $0.attempts >= 0 && $0.chunkCount >= 0 }),
              result.nextAfterID == (ids.last ?? after), !result.hasMore || !ids.isEmpty else {
            throw ServerError.invalidResponse
        }
        return result
    }

    func rebuildAI(_ origin: ServerOrigin, token: String, item: AIIndexItem) async throws {
        struct Item: Encodable { let file_id: Int64; let generation: Int64 }
        struct Input: Encodable { let items: [Item] }
        struct Receipt: Decodable { let status: String; let items: [ItemReceipt] }
        struct ItemReceipt: Decodable { let file_id: Int64; let generation: Int64 }
        guard item.id > 0, item.generation >= 0, item.generation < Int64.max else { throw ServerError.invalidOperation }
        let response: Receipt = try await request(origin, "api/ai/index/rebuild", token: token,
            body: JSONEncoder().encode(Input(items: [Item(file_id: item.id, generation: item.generation)])), method: "POST", successStatus: 202)
        guard response.status == "queued", response.items.count == 1,
              response.items[0].file_id == item.id, response.items[0].generation == item.generation + 1 else {
            throw ServerError.invalidResponse
        }
    }
}
