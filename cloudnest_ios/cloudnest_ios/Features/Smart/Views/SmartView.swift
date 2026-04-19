import SwiftUI

struct SmartView: View {
    @StateObject private var viewModel = SmartViewModel()

    var body: some View {
        ScrollView(showsIndicators: false) {
            VStack(alignment: .leading, spacing: 14) {
                searchHeaderCard
                searchInputCard
                searchResultCard
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 12)
        }
        .navigationTitle("智能")
        .navigationBarTitleDisplayMode(.inline)
        .liquidGlassPage()
    }

    private var searchHeaderCard: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("智能文件搜索")
                .font(.headline)
            Text("直接输入自然语言，例如：找一下上周在北京拍的旅游照片")
                .font(.footnote)
                .foregroundStyle(.secondary)
        }
        .padding(12)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
    }

    private var searchInputCard: some View {
        VStack(alignment: .leading, spacing: 10) {
            textField("输入你想找的内容", text: $viewModel.nlQuestion)
                .onSubmit {
                    Task { await viewModel.runNLSearch() }
                }
            primaryButton("搜索") {
                Task { await viewModel.runNLSearch() }
            }
            Text("支持按地点、时间、内容语义查找，不需要记 file_id 或接口名称。")
                .font(.footnote)
                .foregroundStyle(.secondary)
        }
        .modifier(CardStyle())
    }

    private var searchResultCard: some View {
        VStack(alignment: .leading, spacing: 10) {
            if viewModel.isLoading {
                ProgressView("处理中...")
            }

            if let error = viewModel.errorMessage {
                Text(error)
                    .foregroundStyle(.red)
            }
            if let success = viewModel.successMessage {
                Text(success)
                    .foregroundStyle(.green)
            }

            if !viewModel.nlPlanText.isEmpty {
                Text("已按智能语义理解你的问题")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            searchList(viewModel.nlItems)
        }
        .modifier(CardStyle())
    }

    @ViewBuilder
    private func searchList(_ items: [AISearchItemDTO]) -> some View {
        if items.isEmpty {
            Text("暂无结果")
                .foregroundStyle(.secondary)
        } else {
            VStack(alignment: .leading, spacing: 8) {
                ForEach(items) { item in
                    VStack(alignment: .leading, spacing: 4) {
                        Text(item.filename)
                            .font(.headline)
                        Text("类型：\(item.fileType)  大小：\(item.size)")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                        if !item.ocrSnippet.isEmpty {
                            Text(item.ocrSnippet)
                                .font(.caption)
                                .foregroundStyle(.secondary)
                                .lineLimit(2)
                        }
                    }
                    .padding(8)
                    .background(Color.white.opacity(0.3), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                }
            }
        }
    }

    private func textField(_ title: String, text: Binding<String>) -> some View {
        TextField(title, text: text)
            .textInputAutocapitalization(.never)
            .autocorrectionDisabled(true)
            .padding(.horizontal, 12)
            .padding(.vertical, 10)
            .background(Color.white.opacity(0.35), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
    }

    private func primaryButton(_ title: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Text(title)
                .font(.system(size: 15, weight: .bold, design: .rounded))
                .frame(maxWidth: .infinity)
                .padding(.vertical, 10)
        }
        .buttonStyle(.borderedProminent)
        .disabled(viewModel.isLoading)
    }
}

private struct CardStyle: ViewModifier {
    func body(content: Content) -> some View {
        content
            .padding(12)
            .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: 14, style: .continuous)
                    .stroke(Color.white.opacity(0.55), lineWidth: 1)
            )
    }
}

#Preview {
    NavigationStack {
        SmartView()
    }
}
