import Foundation

struct DownloadIdentity: Sendable {
    let length: Int64
    let etag: String
    let sha256: String

    init(response: HTTPURLResponse) throws {
        guard let etag = response.value(forHTTPHeaderField: "ETag"),
              etag.hasPrefix("\"sha256-"), etag.hasSuffix("\""), etag.utf8.count == 73 else { throw TransferError.invalidDownload }
        let hash = String(etag.dropFirst(8).dropLast())
        guard hash.utf8.allSatisfy({ (48...57).contains($0) || (97...102).contains($0) }),
              let digest = response.value(forHTTPHeaderField: "Repr-Digest"),
              digest.hasPrefix("sha-256=:"), digest.hasSuffix(":"),
              let raw = Data(base64Encoded: String(digest.dropFirst(9).dropLast())),
              raw.count == 32, TransferDisk.hex(raw) == hash,
              response.value(forHTTPHeaderField: "Content-Encoding").map({ $0 == "identity" }) ?? true,
              let length = response.value(forHTTPHeaderField: "Content-Length").flatMap(Int64.init), length >= 0 else { throw TransferError.invalidDownload }
        self.etag = etag; sha256 = hash; self.length = length
    }
}

struct DownloadPlan: Sendable {
    let total: Int64
    let length: Int64
    let etag: String
    let sha256: String
    let replaces: Bool

    init(response: HTTPURLResponse, record: TransferRecord, requestedStart: Int64, requestedEnd: Int64) throws {
        let identity = try DownloadIdentity(response: response)
        let etag = identity.etag, length = identity.length
        self.etag = etag
        sha256 = identity.sha256
        self.length = length
        replaces = response.statusCode == 200
        if replaces {
            total = length
        } else {
            guard response.statusCode == 206,
                  let range = response.value(forHTTPHeaderField: "Content-Range"), range.hasPrefix("bytes ") else {
                throw TransferError.invalidDownload
            }
            let parts = range.dropFirst(6).split(separator: "/", omittingEmptySubsequences: false)
            guard parts.count == 2, let total = Int64(parts[1]), total > 0 else { throw TransferError.invalidDownload }
            let span = parts[0].split(separator: "-", omittingEmptySubsequences: false)
            guard span.count == 2, let start = Int64(span[0]), let end = Int64(span[1]),
                  start == requestedStart, end >= start, end == min(requestedEnd, total - 1),
                  length == end - start + 1,
                  record.etag.map({ $0 == etag && record.totalBytes == total }) ?? (record.confirmedBytes == 0) else {
                throw TransferError.invalidDownload
            }
            self.total = total
        }
        if record.fileVersion != nil {
            guard record.versionSize.map({ $0 == total }) ?? true,
                  record.etag.map({ $0 == etag && record.totalBytes == total }) ?? true else {
                throw TransferError.invalidDownload
            }
        }
    }
}
