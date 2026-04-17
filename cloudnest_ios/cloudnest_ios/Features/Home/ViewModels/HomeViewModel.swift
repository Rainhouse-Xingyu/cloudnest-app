import Foundation
import Combine

/// Home 页面状态与交互逻辑
@MainActor
final class HomeViewModel: ObservableObject {
    @Published var title: String = "概览"
    @Published var photoItems: [HomePhotoItem] = []
    @Published var fileItems: [HomeFileItem] = []
    @Published var capacityInfo: HomeCapacityInfo = HomeCapacityInfo(
        usedSpace: 0,
        totalSpace: nil,
        usedText: "0 B",
        totalText: nil,
        summaryText: "容量概览"
    )
    @Published var statusInfo: HomeStatusInfo = HomeStatusInfo(frpDeviceTotal: 0, frpDeviceEnabled: 0, rateWindowText: "未配置")
    @Published var isLoading: Bool = false
    @Published var errorMessage: String? = nil

    private let service: HomeServiceProtocol
    private let fileSizeFormatter = ByteCountFormatter()

    var capacityInfoText: String {
        if let totalText = capacityInfo.totalText {
            return "已用 \(capacityInfo.usedText) / \(totalText)"
        }
        return "已用 \(capacityInfo.usedText)"
    }

    init(service: HomeServiceProtocol = HomeService()) {
        self.service = service
        fileSizeFormatter.allowedUnits = [.useKB, .useMB, .useGB, .useTB]
        fileSizeFormatter.countStyle = .file
        fileSizeFormatter.includesUnit = true
    }

    func onAppear() {
        title = service.loadTitle()
        Task {
            await reload()
        }
    }

    func reload() async {
        isLoading = true
        errorMessage = nil
        defer { isLoading = false }

        do {
            let dashboard = try await service.fetchDashboard()
            applyDashboard(dashboard)
        } catch {
            errorMessage = "首页加载失败：\(error.localizedDescription)"
            photoItems = []
            fileItems = []
        }
    }

    private func applyDashboard(_ dto: HomeDashboardDTO) {
        // 最近文件：只展示普通文件，按更新时间倒序截取前 5 条。
        let sortedFiles = dto.files.items
            .filter { $0.isFolder == 0 }
            .sorted {
                parseBackendDate($0.updateTime) > parseBackendDate($1.updateTime)
            }
        fileItems = Array(sortedFiles.prefix(5)).map { file in
            HomeFileItem(
                id: file.id,
                name: file.filename,
                sizeText: fileSizeFormatter.string(fromByteCount: file.size),
                updatedAt: relativeTimeText(from: file.updateTime)
            )
        }

        // 最近照片：取最近 8 条 Live Photo 数据。
        let sortedLivePhotos = dto.livePhotos.items.sorted {
            parseBackendDate($0.createTime) > parseBackendDate($1.createTime)
        }
        photoItems = Array(sortedLivePhotos.prefix(8)).map { livePhoto in
            HomePhotoItem(
                id: livePhoto.id,
                title: livePhoto.imageFile.filename,
                capturedAt: relativeTimeText(from: livePhoto.createTime),
                imageFileID: livePhoto.imageFileID,
                videoFileID: livePhoto.videoFileID
            )
        }

        if let storage = dto.storageOverview?.item {
            let totalSpace = storage.hasQuota && storage.totalBytes > 0 ? storage.totalBytes : nil
            let sourceText = readableQuotaSource(storage.quotaSource)
            capacityInfo = HomeCapacityInfo(
                usedSpace: max(storage.usedBytes, 0),
                totalSpace: totalSpace,
                usedText: fileSizeFormatter.string(fromByteCount: max(storage.usedBytes, 0)),
                totalText: totalSpace == nil ? nil : fileSizeFormatter.string(fromByteCount: max(storage.totalBytes, 0)),
                summaryText: storage.hasQuota ? "容量来源：\(sourceText)" : "未配置总容量（仅展示已用）",
                hasQuota: storage.hasQuota,
                quotaSource: storage.quotaSource,
                usagePercent: storage.usagePercent
            )
        } else {
            // 兼容后端未发布/接口异常场景，回退到旧版根目录汇总。
            let fallbackUsedBytes = dto.files.items
                .filter { $0.isFolder == 0 }
                .reduce(Int64(0)) { partialResult, item in
                    partialResult + max(item.size, 0)
                }

            capacityInfo = HomeCapacityInfo(
                usedSpace: fallbackUsedBytes,
                totalSpace: nil,
                usedText: fileSizeFormatter.string(fromByteCount: fallbackUsedBytes),
                totalText: nil,
                summaryText: "容量接口异常，已回退根目录汇总"
            )
        }

        let enabledDevices = dto.frpDevices.items.filter { $0.status == 1 }.count
        let rateWindow = dto.rateWindow.item
        let rateWindowText = rateWindow.enabled ? "\(rateWindow.start)-\(rateWindow.end)" : "已关闭"
        statusInfo = HomeStatusInfo(
            frpDeviceTotal: dto.frpDevices.items.count,
            frpDeviceEnabled: enabledDevices,
            rateWindowText: rateWindowText
        )
    }

    private func relativeTimeText(from backendTime: String) -> String {
        let date = parseBackendDate(backendTime)
        let calendar = Calendar.current
        if calendar.isDateInToday(date) {
            return "今天 " + Self.hmFormatter.string(from: date)
        }
        if calendar.isDateInYesterday(date) {
            return "昨天 " + Self.hmFormatter.string(from: date)
        }
        return Self.mdHmFormatter.string(from: date)
    }

    private func parseBackendDate(_ text: String) -> Date {
        if let date = Self.primaryDateFormatter.date(from: text) {
            return date
        }
        if let date = Self.iso8601Formatter.date(from: text) {
            return date
        }
        return .distantPast
    }

    private func readableQuotaSource(_ source: String) -> String {
        switch source {
        case "storage_quota_bytes":
            return "全局配额"
        case "storage_default_quota_bytes":
            return "默认配额"
        case "none":
            return "未配置"
        default:
            return source
        }
    }

    private static let primaryDateFormatter: DateFormatter = {
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "zh_CN")
        formatter.timeZone = TimeZone.current
        formatter.dateFormat = "yyyy-MM-dd'T'HH:mm:ssXXX"
        return formatter
    }()

    private static let iso8601Formatter = ISO8601DateFormatter()

    private static let hmFormatter: DateFormatter = {
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "zh_CN")
        formatter.timeZone = TimeZone.current
        formatter.dateFormat = "HH:mm"
        return formatter
    }()

    private static let mdHmFormatter: DateFormatter = {
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "zh_CN")
        formatter.timeZone = TimeZone.current
        formatter.dateFormat = "MM-dd HH:mm"
        return formatter
    }()
}
