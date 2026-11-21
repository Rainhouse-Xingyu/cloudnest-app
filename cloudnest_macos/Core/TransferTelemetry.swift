import Foundation

// Per-run, in-memory HTTP body measurements. Delegate callbacks never publish
// observable state or enqueue a MainActor task for every network block.
final class TransferTelemetry: @unchecked Sendable {
    enum Stage: Sendable {
        case preparing, checking, transferring, confirming, verifying

        var title: String {
            switch self {
            case .preparing: "正在准备文件"
            case .checking: "正在核对传输状态"
            case .transferring: "正在传输"
            case .confirming: "正在确认上传结果"
            case .verifying: "正在校验并保存文件"
            }
        }
    }

    struct Snapshot: Sendable {
        let stage: Stage
        let bodyBytes: Int64
        let bytesPerSecond: Double?
        let bufferedSamples: Int
    }

    private struct Bucket {
        let start: ContinuousClock.Instant
        var end: ContinuousClock.Instant
        var bytes: Int64
    }

    private let lock = NSLock()
    private var active = true
    private var stage: Stage = .preparing
    private var started: ContinuousClock.Instant?
    private var latest: ContinuousClock.Instant
    private var bodyBytes: Int64 = 0
    private var buckets: [Bucket] = []

    init(at instant: ContinuousClock.Instant = .now) { latest = instant }

    static func formattedRate(_ value: Double) -> String {
        guard value.isFinite, value >= 0 else { return "—" }
        let bytes = value >= Double(Int64.max) ? Int64.max : Int64(value.rounded(.down))
        return bytes.formatted(.byteCount(style: .file))
    }

    func transition(to stage: Stage, at instant: ContinuousClock.Instant? = nil) {
        lock.lock()
        defer { lock.unlock() }
        guard active else { return }
        let now = max(instant ?? .now, latest)
        latest = now
        if stage == .transferring && self.stage != .transferring {
            started = now
            bodyBytes = 0
            buckets.removeAll(keepingCapacity: true)
        }
        self.stage = stage
    }

    func restartRepresentation(at instant: ContinuousClock.Instant? = nil) {
        lock.lock()
        defer { lock.unlock() }
        guard active, stage == .transferring else { return }
        latest = max(instant ?? .now, latest)
        started = latest
        bodyBytes = 0
        buckets.removeAll(keepingCapacity: true)
    }

    func record(bytes: Int64, at instant: ContinuousClock.Instant? = nil) {
        lock.lock()
        defer { lock.unlock() }
        guard active, stage == .transferring, bytes > 0 else { return }
        let now = instant ?? .now
        guard now >= latest else { return }
        latest = now
        prune(at: now)
        let accepted = min(bytes, Int64.max - bodyBytes)
        guard accepted > 0 else { return }
        bodyBytes += accepted
        if let last = buckets.last, last.start.duration(to: now) < .milliseconds(200) {
            buckets[buckets.count - 1].end = now
            buckets[buckets.count - 1].bytes += accepted
        } else {
            buckets.append(Bucket(start: now, end: now, bytes: accepted))
        }
    }

    func snapshot(at instant: ContinuousClock.Instant? = nil) -> Snapshot {
        lock.lock()
        defer { lock.unlock() }
        let now = max(instant ?? .now, latest)
        latest = now
        prune(at: now)
        var rate: Double?
        if active, stage == .transferring, let started {
            let duration = started.duration(to: now).components
            let elapsed = Double(duration.seconds) + Double(duration.attoseconds) / 1e18
            if elapsed >= 0.25 {
                // A boundary bucket may overlap the five-second cutoff by less
                // than 200 ms. The buffer stays bounded to at most 27 buckets.
                let recent = buckets.reduce(Int64(0)) { $0 + $1.bytes }
                rate = Double(recent) / min(5, elapsed)
            }
        }
        return Snapshot(stage: stage, bodyBytes: bodyBytes, bytesPerSecond: rate, bufferedSamples: buckets.count)
    }

    func stop() {
        lock.lock()
        defer { lock.unlock() }
        active = false
        buckets.removeAll()
    }

    private func prune(at now: ContinuousClock.Instant) {
        let cutoff = now.advanced(by: .seconds(-5))
        buckets.removeAll { $0.end <= cutoff }
    }
}

// Preserve certificate validation and the no-redirect policy while observing
// only the upload body callback of this particular task.
final class UploadProgressDelegate: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
    private let policy: any URLSessionTaskDelegate
    private let telemetry: TransferTelemetry

    init(policy: any URLSessionTaskDelegate, telemetry: TransferTelemetry) {
        self.policy = policy
        self.telemetry = telemetry
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didSendBodyData bytesSent: Int64,
                    totalBytesSent: Int64, totalBytesExpectedToSend: Int64) {
        telemetry.record(bytes: bytesSent)
    }

    func urlSession(_ session: URLSession, task: URLSessionTask,
                    willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest,
                    completionHandler: @escaping @Sendable (URLRequest?) -> Void) {
        completionHandler(nil)
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didReceive challenge: URLAuthenticationChallenge,
                    completionHandler: @escaping @Sendable (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        policy.urlSession?(session, task: task, didReceive: challenge, completionHandler: completionHandler)
            ?? completionHandler(.performDefaultHandling, nil)
    }
}
