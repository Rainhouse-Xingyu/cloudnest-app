import SwiftUI

struct TransfersView: View {
    @StateObject private var viewModel = TransfersViewModel()
    @ObservedObject private var manager = TransferManager.shared
    
    var body: some View {
        VStack {
            Picker("传输类型", selection: $viewModel.selectedSegment) {
                Text("上传").tag(0)
                Text("下载").tag(1)
            }
            .pickerStyle(SegmentedPickerStyle())
            .padding()
            
            List {
                let tasks = viewModel.selectedSegment == 0 ? viewModel.uploadTasks : viewModel.downloadTasks
                
                if tasks.isEmpty {
                    Text(viewModel.selectedSegment == 0 ? "暂无上传任务" : "暂无下载任务")
                        .foregroundColor(.gray)
                        .padding()
                } else {
                    ForEach(tasks) { task in
                        HStack {
                            VStack(alignment: .leading) {
                                Text(task.name)
                                    .font(.headline)
                                    .lineLimit(1)
                                
                                switch task.status {
                                case .pending:
                                    Text("等待中")
                                        .font(.subheadline)
                                        .foregroundColor(.gray)
                                case .inProgress(let progress, let speed):
                                    VStack(alignment: .leading, spacing: 4) {
                                        HStack(spacing: 8) {
                                            ProgressView(value: progress)
                                            Text("\(Int(progress * 100))%")
                                                .font(.subheadline)
                                                .foregroundColor(.secondary)
                                        }
                                        Text(speed)
                                            .font(.caption)
                                            .foregroundColor(.secondary)
                                    }
                                case .paused(let progress):
                                    HStack(spacing: 8) {
                                        ProgressView(value: progress)
                                        Text("\(Int(progress * 100))%")
                                            .font(.subheadline)
                                            .foregroundColor(.secondary)
                                        Text("已暂停")
                                            .font(.caption)
                                            .foregroundColor(.orange)
                                    }
                                case .completed:
                                    Text("已完成")
                                        .font(.subheadline)
                                        .foregroundColor(.green)
                                case .failed(let error):
                                    Text("失败 - \(error)")
                                        .font(.subheadline)
                                        .foregroundColor(.red)
                                }
                            }
                            
                            Spacer()
                            
                            // 操作按钮
                            if case .inProgress = task.status {
                                Button(action: {
                                    manager.pauseTask(id: task.id)
                                }) {
                                    Image(systemName: "pause.circle.fill")
                                        .font(.title2)
                                        .foregroundColor(.blue)
                                }
                            } else if case .paused = task.status {
                                Button(action: {
                                    manager.resumeTask(id: task.id)
                                }) {
                                    Image(systemName: "play.circle.fill")
                                        .font(.title2)
                                        .foregroundColor(.blue)
                                }
                            } else if case .pending = task.status {
                                Button(action: {
                                    manager.pauseTask(id: task.id)
                                }) {
                                    Image(systemName: "pause.circle.fill")
                                        .font(.title2)
                                        .foregroundColor(.gray)
                                }
                            }
                        }
                        .padding(.vertical, 4)
                    }
                }
            }
            .listStyle(PlainListStyle())
        }
        .navigationTitle("传输记录")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .navigationBarTrailing) {
                let hasActive = manager.tasks.contains(where: {
                    if case .inProgress = $0.status { return true }
                    if case .pending = $0.status { return true }
                    return false
                })
                
                Button(action: {
                    if hasActive {
                        manager.pauseAll()
                    } else {
                        manager.resumeAll()
                    }
                }) {
                    Text(hasActive ? "全部暂停" : "全部开始")
                }
            }
        }
    }
}
