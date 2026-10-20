import Foundation
import Combine

enum TransferType {
    case upload
    case download
}

enum TransferStatus: Equatable {
    case pending
    case inProgress(progress: Double, speed: String)
    case paused(progress: Double)
    case completed
    case failed(error: String)
}

struct TransferTask: Identifiable {
    let id: UUID = UUID()
    let name: String
    let type: TransferType
    var status: TransferStatus
    
    var totalSize: Int64 = 0
    var lastUpdateTime: Date = Date()
    var lastProgress: Double = 0.0
    var currentSpeed: String = "计算中..."
    
    var uploadId: String? // 用于断点续传
}

@MainActor
class TransferManager: ObservableObject {
    static let shared = TransferManager()
    
    @Published var tasks: [TransferTask] = []
    
    private var activeTasks: [UUID: Task<Void, Never>] = [:]
    private var resumeBlocks: [UUID: @Sendable () async -> Void] = [:]
    
    private init() {}
    
    func addTask(name: String, type: TransferType, totalSize: Int64 = 0) -> UUID {
        let task = TransferTask(name: name, type: type, status: .pending, totalSize: totalSize, lastUpdateTime: Date(), lastProgress: 0.0, currentSpeed: "计算中...")
        tasks.append(task)
        return task.id
    }
    
    func updateTaskProgress(id: UUID, progress: Double) {
        if let index = tasks.firstIndex(where: { $0.id == id }) {
            let now = Date()
            var task = tasks[index]
            
            let timeDelta = now.timeIntervalSince(task.lastUpdateTime)
            // 至少 0.2 秒更新一次网速，避免数值剧烈跳动
            if timeDelta > 0.2 {
                let progressDelta = progress - task.lastProgress
                if progressDelta > 0 && task.totalSize > 0 {
                    let bytesDelta = Double(task.totalSize) * progressDelta
                    let speedBytesPerSec = bytesDelta / timeDelta
                    task.currentSpeed = formatSpeed(speedBytesPerSec)
                }
                task.lastUpdateTime = now
                task.lastProgress = progress
            }
            
            task.status = .inProgress(progress: progress, speed: task.currentSpeed)
            tasks[index] = task
        }
    }
    
    private func formatSpeed(_ bytesPerSec: Double) -> String {
        let kb = bytesPerSec / 1024
        if kb > 1024 {
            let mb = kb / 1024
            return String(format: "%.1f MB/s", mb)
        } else {
            return String(format: "%.1f KB/s", kb)
        }
    }
    
    func completeTask(id: UUID) {
        if let index = tasks.firstIndex(where: { $0.id == id }) {
            tasks[index].status = .completed
        }
        activeTasks.removeValue(forKey: id)
        resumeBlocks.removeValue(forKey: id)
    }
    
    func failTask(id: UUID, error: String) {
        if let index = tasks.firstIndex(where: { $0.id == id }) {
            tasks[index].status = .failed(error: error)
        }
        activeTasks.removeValue(forKey: id)
    }
    
    func setUploadId(id: UUID, uploadId: String) {
        if let index = tasks.firstIndex(where: { $0.id == id }) {
            tasks[index].uploadId = uploadId
        }
    }
    
    func registerTaskExecution(id: UUID, task: Task<Void, Never>, resumeBlock: @escaping @Sendable () async -> Void) {
        activeTasks[id] = task
        resumeBlocks[id] = resumeBlock
    }
    
    func pauseTask(id: UUID) {
        if let index = tasks.firstIndex(where: { $0.id == id }) {
            activeTasks[id]?.cancel()
            activeTasks.removeValue(forKey: id)
            let currentProg = tasks[index].lastProgress
            tasks[index].status = .paused(progress: currentProg)
        }
    }
    
    func resumeTask(id: UUID) {
        if let index = tasks.firstIndex(where: { $0.id == id }) {
            tasks[index].status = .pending
            if let block = resumeBlocks[id] {
                let task = Task {
                    await block()
                }
                activeTasks[id] = task
            }
        }
    }
    
    func pauseAll() {
        for task in tasks {
            if case .inProgress = task.status {
                pauseTask(id: task.id)
            } else if case .pending = task.status {
                pauseTask(id: task.id)
            }
        }
    }
    
    func resumeAll() {
        for task in tasks {
            if case .paused = task.status {
                resumeTask(id: task.id)
            }
        }
    }
}
