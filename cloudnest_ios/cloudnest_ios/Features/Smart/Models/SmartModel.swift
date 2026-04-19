import Foundation

// MARK: - Request DTOs

struct AIOCRRequestDTO: Encodable {
    let fileId: Int64
    let location: String
    let landmark: String
    let capturedAt: String
    let tags: String
    let description: String

    enum CodingKeys: String, CodingKey {
        case fileId = "file_id"
        case location
        case landmark
        case capturedAt = "captured_at"
        case tags
        case description
    }
}

struct AINLSearchRequestDTO: Encodable {
    let question: String
    let page: Int
    let limit: Int
}

struct AISummarizeRequestDTO: Encodable {
    let fileId: Int64
    let maxPoints: Int

    enum CodingKeys: String, CodingKey {
        case fileId = "file_id"
        case maxPoints = "max_points"
    }
}

struct AIAssistantAskRequestDTO: Encodable {
    let question: String
}

struct AIAssistantSettingRequestDTO: Encodable {
    let settingKey: String
    let settingValue: String
    let description: String

    enum CodingKeys: String, CodingKey {
        case settingKey = "setting_key"
        case settingValue = "setting_value"
        case description
    }
}

// MARK: - Response DTOs

struct AIOCRResponseDTO: Decodable {
    let item: AIOCRItemDTO
    let exif: AIOCRExifDTO
    let docSummary: String

    enum CodingKeys: String, CodingKey {
        case item
        case exif
        case docSummary = "doc_summary"
    }
}

struct AIOCRItemDTO: Decodable {
    let id: Int64
    let fileId: Int64
    let tags: String
    let description: String
    let ocrText: String

    enum CodingKeys: String, CodingKey {
        case id
        case fileId = "file_id"
        case tags
        case description
        case ocrText = "ocr_text"
    }
}

struct AIOCRExifDTO: Decodable {
    let latitude: Double?
    let longitude: Double?
    let altitude: Double?
    let capturedAt: String?
    let cameraMake: String?
    let cameraModel: String?

    enum CodingKeys: String, CodingKey {
        case latitude
        case longitude
        case altitude
        case capturedAt = "captured_at"
        case cameraMake = "camera_make"
        case cameraModel = "camera_model"
    }
}

struct AISearchResponseDTO: Decodable {
    let items: [AISearchItemDTO]
    let pagination: AIPaginationDTO
}

struct AISearchItemDTO: Decodable, Identifiable {
    let id: Int64
    let filename: String
    let fileType: String
    let size: Int64
    let tags: String
    let description: String
    let ocrSnippet: String
    let hasOCRIndex: Bool

    enum CodingKeys: String, CodingKey {
        case id
        case filename
        case fileType = "file_type"
        case size
        case tags
        case description
        case ocrSnippet = "ocr_snippet"
        case hasOCRIndex = "has_ocr_index"
    }
}

struct AIPaginationDTO: Decodable {
    let page: Int
    let limit: Int
    let total: Int64
}

struct AINLSearchResponseDTO: Decodable {
    let items: [AISearchItemDTO]
    let pagination: AIPaginationDTO
    let nlPlan: AINLPlanDTO

    enum CodingKeys: String, CodingKey {
        case items
        case pagination
        case nlPlan = "nl_plan"
    }
}

struct AINLPlanDTO: Decodable {
    let source: String
    let query: AINLQueryDTO
}

struct AINLQueryDTO: Decodable {
    let keyword: String
    let fileType: String
    let location: String
    let landmark: String
    let startTime: String
    let endTime: String
    let tagOnly: Bool
    let page: Int
    let limit: Int

    enum CodingKeys: String, CodingKey {
        case keyword
        case fileType = "file_type"
        case location
        case landmark
        case startTime = "start_time"
        case endTime = "end_time"
        case tagOnly = "tag_only"
        case page
        case limit
    }
}

struct AISummaryResponseDTO: Decodable {
    let item: AISummaryItemDTO
}

struct AISummaryItemDTO: Decodable {
    let fileId: Int64
    let filename: String
    let summary: String
    let truncated: Bool
    let maxPoints: Int
    let model: String

    enum CodingKeys: String, CodingKey {
        case fileId = "file_id"
        case filename
        case summary
        case truncated
        case maxPoints = "max_points"
        case model
    }
}

struct AIAssistantAskResponseDTO: Decodable {
    let item: AIAssistantAskItemDTO
}

struct AIAssistantAskItemDTO: Decodable {
    let answer: String
    let model: String
    let guideDocLoaded: Bool

    enum CodingKeys: String, CodingKey {
        case answer
        case model
        case guideDocLoaded = "guide_doc_loaded"
    }
}

struct AISettingResponseDTO: Decodable {
    let message: String
}

// MARK: - UI Models

enum SmartAIMode: String, CaseIterable, Identifiable {
    case ocr
    case search
    case nlSearch
    case summarize
    case assistant
    case setting

    var id: String { rawValue }

    var title: String {
        switch self {
        case .ocr: return "OCR"
        case .search: return "智能搜索"
        case .nlSearch: return "自然语言"
        case .summarize: return "文档摘要"
        case .assistant: return "AI 助手"
        case .setting: return "AI 设置"
        }
    }
}
