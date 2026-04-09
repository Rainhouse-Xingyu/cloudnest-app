import SwiftUI

/// Auth 模块主页面（模板）
struct AuthView: View {
    @StateObject private var viewModel = AuthViewModel()

    var body: some View {
        VStack {
            FeatureGlassTemplateView(
                icon: "person.badge.key.fill",
                title: viewModel.title,
                subtitle: "这里是 Auth 功能模板，请替换为真实业务 UI"
            )
        }
        .padding()
        .liquidGlassPage()
        .onAppear {
            viewModel.onAppear()
        }
    }
}

struct AuthView_Previews: PreviewProvider {
    static var previews: some View {
        AuthView()
    }
}
