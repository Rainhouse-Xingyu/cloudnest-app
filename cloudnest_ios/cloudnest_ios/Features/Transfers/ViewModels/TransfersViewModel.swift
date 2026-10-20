import Foundation
import Combine

@MainActor
final class TransfersViewModel: ObservableObject {
    @Published var selectedSegment: Int = 0 // 0 for upload, 1 for download
    
    var uploadTasks: [TransferTask] {
        TransferManager.shared.tasks.filter { $0.type == .upload }.reversed()
    }
    
    var downloadTasks: [TransferTask] {
        TransferManager.shared.tasks.filter { $0.type == .download }.reversed()
    }
}
