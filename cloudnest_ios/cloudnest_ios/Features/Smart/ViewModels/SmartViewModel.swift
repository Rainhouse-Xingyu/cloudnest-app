import Foundation
import Combine

@MainActor
final class SmartViewModel: ObservableObject {
    @Published var mode: SmartAIMode = .ocr
    @Published var isLoading: Bool = false
    @Published var errorMessage: String?
    @Published var successMessage: String?

    // OCR
    @Published var ocrFileIdInput: String = ""
    @Published var ocrLocation: String = ""
    @Published var ocrLandmark: String = ""
    @Published var ocrCapturedAt: String = ""
    @Published var ocrTags: String = ""
    @Published var ocrDescription: String = ""
    @Published var ocrResultText: String = ""

    // Search
    @Published var searchKeyword: String = ""
    @Published var searchFileType: String = ""
    @Published var searchLocation: String = ""
    @Published var searchLandmark: String = ""
    @Published var searchStartTime: String = ""
    @Published var searchEndTime: String = ""
    @Published var searchTagOnly: Bool = false
    @Published var searchItems: [AISearchItemDTO] = []

    // NL Search
    @Published var nlQuestion: String = ""
    @Published var nlPlanText: String = ""
    @Published var nlItems: [AISearchItemDTO] = []

    // Summarize
    @Published var summaryFileIdInput: String = ""
    @Published var summaryMaxPointsInput: String = "6"
    @Published var summaryText: String = ""

    // Assistant
    @Published var assistantQuestion: String = ""
    @Published var assistantAnswer: String = ""

    // AI Setting
    @Published var settingKey: String = ""
    @Published var settingValue: String = ""
    @Published var settingDescription: String = ""

    private let service: SmartServiceProtocol

    init(service: SmartServiceProtocol = SmartService()) {
        self.service = service
    }

    func runOCR() async {
        guard let fileId = Int64(ocrFileIdInput.trimmingCharacters(in: .whitespacesAndNewlines)), fileId > 0 else {
            errorMessage = "请输入正确的 file_id"
            return
        }

        await runTask {
            let req = AIOCRRequestDTO(
                fileId: fileId,
                location: ocrLocation,
                landmark: ocrLandmark,
                capturedAt: ocrCapturedAt,
                tags: ocrTags,
                description: ocrDescription
            )
            let res = try await service.ocr(req)
            ocrResultText = res.item.ocrText
            successMessage = "OCR 完成"
        }
    }

    func runSearch() async {
        await runTask {
            let res = try await service.search(
                keyword: searchKeyword,
                fileType: searchFileType,
                location: searchLocation,
                landmark: searchLandmark,
                startTime: searchStartTime,
                endTime: searchEndTime,
                tagOnly: searchTagOnly,
                page: 1,
                limit: 20
            )
            searchItems = res.items
            successMessage = "搜索完成，共 \(res.pagination.total) 条"
        }
    }

    func runNLSearch() async {
        let q = nlQuestion.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !q.isEmpty else {
            errorMessage = "请输入自然语言问题"
            return
        }

        await runTask {
            let res = try await service.nlSearch(AINLSearchRequestDTO(question: q, page: 1, limit: 20))
            nlItems = res.items
            nlPlanText = "source=\(res.nlPlan.source), keyword=\(res.nlPlan.query.keyword), file_type=\(res.nlPlan.query.fileType), location=\(res.nlPlan.query.location), landmark=\(res.nlPlan.query.landmark), tag_only=\(res.nlPlan.query.tagOnly)"
            successMessage = "自然语言检索完成，共 \(res.pagination.total) 条"
        }
    }

    func runSummarize() async {
        guard let fileId = Int64(summaryFileIdInput.trimmingCharacters(in: .whitespacesAndNewlines)), fileId > 0 else {
            errorMessage = "请输入正确的 file_id"
            return
        }
        let maxPoints = Int(summaryMaxPointsInput.trimmingCharacters(in: .whitespacesAndNewlines)) ?? 6

        await runTask {
            let res = try await service.summarize(AISummarizeRequestDTO(fileId: fileId, maxPoints: maxPoints))
            summaryText = res.item.summary
            successMessage = "摘要完成"
        }
    }

    func runAssistantAsk() async {
        let q = assistantQuestion.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !q.isEmpty else {
            errorMessage = "请输入问题"
            return
        }

        await runTask {
            let res = try await service.askAssistant(AIAssistantAskRequestDTO(question: q))
            assistantAnswer = res.item.answer
            successMessage = "问答完成"
        }
    }

    func runApplySetting() async {
        let key = settingKey.trimmingCharacters(in: .whitespacesAndNewlines)
        let value = settingValue.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !key.isEmpty, !value.isEmpty else {
            errorMessage = "setting_key 与 setting_value 不能为空"
            return
        }

        await runTask {
            let res = try await service.applySetting(
                AIAssistantSettingRequestDTO(
                    settingKey: key,
                    settingValue: value,
                    description: settingDescription
                )
            )
            successMessage = res.message
        }
    }

    private func runTask(_ work: () async throws -> Void) async {
        isLoading = true
        errorMessage = nil
        successMessage = nil
        defer { isLoading = false }

        do {
            try await work()
        } catch {
            errorMessage = error.localizedDescription
        }
    }
}
