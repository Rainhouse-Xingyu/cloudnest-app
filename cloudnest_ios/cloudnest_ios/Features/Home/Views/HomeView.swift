import SwiftUI

/// Home 模块主页面（模板）
struct HomeView: View {
    @StateObject private var viewModel = HomeViewModel()

    var body: some View {
        VStack {
            FeatureGlassTemplateView(
                icon: "sparkles.rectangle.stack.fill",
                title: viewModel.title,
                subtitle: "这里是 Home 功能模板，请替换为真实业务 UI"
            )
        }
        .padding()
        .liquidGlassPage()
        .onAppear {
            viewModel.onAppear()
        }
    }
}

struct HomeView_Previews: PreviewProvider {
    static var previews: some View {
        HomeView()
    }
}
