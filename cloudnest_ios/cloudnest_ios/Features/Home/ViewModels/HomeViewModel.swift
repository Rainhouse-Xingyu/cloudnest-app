import Foundation
import Combine

/// Home 页面状态与交互逻辑
@MainActor
final class HomeViewModel: ObservableObject {
    @Published var title: String = "概览"
    @Published var photoItems: [HomePhotoItem] = []
    @Published var fileItems: [HomeFileItem] = []
    @Published var capacityInfo: HomeCapacityInfo = HomeCapacityInfo(usedSpace: 0, totalSpace: 1, usedText: "0 MB", totalText: "0 GB")

    private let service: HomeServiceProtocol

    init(service: HomeServiceProtocol = HomeService()) {
        self.service = service
    }

    func onAppear() {
        title = service.loadTitle()
        loadMockSections()
    }

    /// 首页分类数据（后续可替换为接口返回）
    private func loadMockSections() {
        photoItems = [
            HomePhotoItem(title: "校园春景", capturedAt: "今天 09:10"),
            HomePhotoItem(title: "实验室白板", capturedAt: "昨天 18:22"),
            HomePhotoItem(title: "项目演示", capturedAt: "04-08 14:05")
        ]

        fileItems = [
            HomeFileItem(name: "需求文档-v3.pdf", sizeText: "2.4 MB", updatedAt: "今天 11:30"),
            HomeFileItem(name: "毕业答辩PPT.key", sizeText: "18.2 MB", updatedAt: "昨天 21:07"),
            HomeFileItem(name: "云盘接口清单.xlsx", sizeText: "860 KB", updatedAt: "04-08 16:42")
        ]
        
        capacityInfo = HomeCapacityInfo(
            usedSpace: 25_120_000_000,
            totalSpace: 50_000_000_000,
            usedText: "23.4 GB",
            totalText: "50 GB"
        )
    }
}
