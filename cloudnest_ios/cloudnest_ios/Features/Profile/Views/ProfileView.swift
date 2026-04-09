import SwiftUI

/// Profile 模块主页面（模板）
struct ProfileView: View {
    @StateObject private var viewModel = ProfileViewModel()

    var body: some View {
        VStack {
            FeatureGlassTemplateView(
                icon: "person.text.rectangle.fill",
                title: viewModel.title,
                subtitle: "这里是 Profile 功能模板，请替换为真实业务 UI"
            )
        }
        .padding()
        .liquidGlassPage()
        .onAppear {
            viewModel.onAppear()
        }
    }
}

struct ProfileView_Previews: PreviewProvider {
    static var previews: some View {
        ProfileView()
    }
}
