import Foundation
import Observation

@MainActor @Observable
final class PublicShareState {
    private let client: PublicShareClient
    private let downloader = PublicShareDownloader()
    private var operation: Task<Void, Never>?
    private var generation = UUID()
    private var confirmedPasscode = ""
    private var clearWhenIdle = false
    private(set) var metadata: PublicShareMetadata?
    private(set) var busy = false
    private(set) var downloading = false
    private(set) var cancelling = false
    private(set) var verifying = false
    private(set) var received: Int64 = 0
    private(set) var total: Int64 = 0
    private(set) var receipt: PublicShareDownloadReceipt?
    private(set) var error: String?

    init(client: PublicShareClient) { self.client = client }

    func load(target: PublicShareTarget, passcode: String) async {
        guard !downloading else { return }
        operation?.cancel()
        let id = UUID(); generation = id
        busy = true; error = nil; metadata = nil; receipt = nil; confirmedPasscode = ""
        let task = Task {
            defer { if generation == id { busy = false; operation = nil; if clearWhenIdle { clear() } } }
            do {
                let result = try await client.metadata(target, passcode: passcode)
                try Task.checkCancellation()
                guard generation == id else { return }
                metadata = result; confirmedPasscode = passcode
            } catch is CancellationError { return }
            catch {
                guard generation == id else { return }
                self.error = (error as? PublicShareError)?.localizedDescription ?? "分享读取未完成，请检查连接。"
            }
        }
        operation = task; await task.value
    }

    func download(_ selected: PublicShareMetadata, to destination: URL) async {
        guard !busy, let metadata else { return }
        guard metadata == selected else { error = "分享资料已变化，请重新读取并选择保存位置。"; return }
        let id = UUID(); generation = id
        let passcode = confirmedPasscode
        busy = true; downloading = true; cancelling = false; verifying = false; received = 0; total = 0; receipt = nil; error = nil
        let task = Task {
            defer { if generation == id { busy = false; operation = nil; downloading = false; cancelling = false; verifying = false; if clearWhenIdle { clear() } } }
            do {
                let result = try await downloader.download(client: client, metadata: metadata, passcode: passcode, destination: destination) { received, total, verifying in
                    await self.update(id, received: received, total: total, verifying: verifying)
                }
                guard generation == id else { return }
                receipt = result
                if result.cleanupIncomplete { error = "文件已验证并保存，但临时副本未能清理，请检查目录权限。" }
            } catch is CancellationError {
                if generation == id { error = "下载已取消，已开始的下载仍可能计入次数。" }
            } catch {
                guard generation == id else { return }
                self.error = (error as? PublicShareTransferFailure)?.localizedDescription
                    ?? (error as? PublicShareError)?.localizedDescription
                    ?? (error as? TransferError)?.localizedDescription ?? "下载未完成，请检查连接与本地空间。"
            }
        }
        operation = task; await task.value
    }

    private func update(_ id: UUID, received: Int64, total: Int64, verifying: Bool) {
        guard generation == id else { return }
        self.received = received; self.total = total; self.verifying = verifying
    }

    func cancel() {
        guard busy else { return }
        if downloading { cancelling = true }
        operation?.cancel()
    }

    func clear() {
        guard !busy else { clearWhenIdle = true; cancel(); return }
        generation = UUID(); operation = nil; metadata = nil; confirmedPasscode = ""; receipt = nil; error = nil
        clearWhenIdle = false; received = 0; total = 0; verifying = false
    }
}
