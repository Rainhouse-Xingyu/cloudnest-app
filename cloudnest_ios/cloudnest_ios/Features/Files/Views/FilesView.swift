import SwiftUI

/// Files 模块主页面（模板）
struct FilesView: View {
    @StateObject private var viewModel = FilesViewModel()

    var body: some View {
        VStack {
            FeatureGlassTemplateView(
                icon: "doc.on.doc.fill",
                title: viewModel.title,
                subtitle: "这里是 Files 功能模板，请替换为真实业务 UI"
            )
        }
        .padding()
        .liquidGlassPage()
        .onAppear {
            viewModel.onAppear()
        }
    }
}

struct FilesView_Previews: PreviewProvider {
    static var previews: some View {
        FilesView()
    }
}
