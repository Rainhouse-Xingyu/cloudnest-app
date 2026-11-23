import Foundation

// A dedicated delegate queue delivers one Data block at a time. The consumer
// acknowledges each block after processing it, keeping network buffering bounded.
final class DownloadStream: NSObject, URLSessionDataDelegate, @unchecked Sendable {
    struct Chunk: Sendable {
        let data: Data
        let acknowledge: @Sendable () -> Void
    }

    let chunks: AsyncThrowingStream<Chunk, any Error>
    private let continuation: AsyncThrowingStream<Chunk, any Error>.Continuation
    private let policy: any URLSessionTaskDelegate
    private let gate = NSCondition()
    private var waiting = false
    private var finished = false
    private var responseWaiter: CheckedContinuation<HTTPURLResponse, any Error>?
    private var connection: URLSession?
    private var task: URLSessionDataTask?

    init(configuration: URLSessionConfiguration, policy: any URLSessionTaskDelegate) {
        let (chunks, continuation) = AsyncThrowingStream<Chunk, any Error>.makeStream(bufferingPolicy: .bufferingOldest(1))
        self.chunks = chunks
        self.continuation = continuation
        self.policy = policy
        super.init()
        let queue = OperationQueue()
        queue.maxConcurrentOperationCount = 1
        connection = URLSession(configuration: configuration, delegate: self, delegateQueue: queue)
        continuation.onTermination = { [weak self] _ in self?.cancel() }
    }

    func start(_ request: URLRequest) async throws -> HTTPURLResponse {
        try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { waiter in
                gate.lock()
                guard !finished, let connection else {
                    gate.unlock()
                    waiter.resume(throwing: CancellationError())
                    return
                }
                responseWaiter = waiter
                let task = connection.dataTask(with: request)
                self.task = task
                gate.unlock()
                task.resume()
            }
        } onCancel: { self.cancel() }
    }

    func cancel() {
        gate.lock()
        guard !finished else { gate.unlock(); return }
        finished = true
        waiting = false
        let waiter = responseWaiter
        responseWaiter = nil
        let connection = self.connection
        self.connection = nil
        gate.broadcast()
        gate.unlock()
        waiter?.resume(throwing: CancellationError())
        continuation.finish(throwing: CancellationError())
        connection?.invalidateAndCancel()
    }

    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive response: URLResponse,
                    completionHandler: @escaping @Sendable (URLSession.ResponseDisposition) -> Void) {
        gate.lock()
        let waiter = responseWaiter
        responseWaiter = nil
        let active = !finished
        gate.unlock()
        guard active, let response = response as? HTTPURLResponse else {
            waiter?.resume(throwing: ServerError.invalidResponse)
            completionHandler(.cancel)
            return
        }
        waiter?.resume(returning: response)
        completionHandler(.allow)
    }

    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
        gate.lock()
        guard !finished else { gate.unlock(); return }
        waiting = true
        gate.unlock()
        let chunk = Chunk(data: data) { [weak self] in
            guard let self else { return }
            self.gate.lock()
            self.waiting = false
            self.gate.broadcast()
            self.gate.unlock()
        }
        if case .dropped = continuation.yield(chunk) {
            cancel()
            return
        }
        gate.lock()
        while waiting && !finished { gate.wait() }
        gate.unlock()
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: (any Error)?) {
        gate.lock()
        // cancel() owns the terminal result once it sets finished, even if
        // waking the delegate lets this callback run before finish(throwing:).
        guard !finished else {
            gate.unlock()
            session.finishTasksAndInvalidate()
            return
        }
        finished = true
        waiting = false
        let waiter = responseWaiter
        responseWaiter = nil
        connection = nil
        gate.broadcast()
        gate.unlock()
        waiter?.resume(throwing: error ?? ServerError.invalidResponse)
        if let error { continuation.finish(throwing: error) }
        else { continuation.finish() }
        session.finishTasksAndInvalidate()
    }

    func urlSession(_ session: URLSession, task: URLSessionTask,
                    willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest, completionHandler: @escaping @Sendable (URLRequest?) -> Void) {
        completionHandler(nil)
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didReceive challenge: URLAuthenticationChallenge,
                    completionHandler: @escaping @Sendable (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        policy.urlSession?(session, task: task, didReceive: challenge, completionHandler: completionHandler)
            ?? completionHandler(.performDefaultHandling, nil)
    }

    func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge,
                    completionHandler: @escaping @Sendable (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        gate.lock()
        let task = self.task
        gate.unlock()
        guard let task else { completionHandler(.cancelAuthenticationChallenge, nil); return }
        self.urlSession(session, task: task, didReceive: challenge, completionHandler: completionHandler)
    }
}
