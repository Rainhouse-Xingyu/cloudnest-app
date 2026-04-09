import SwiftUI

/// Settings 模块主页面（模板）
struct SettingsView: View {
    @StateObject private var viewModel = SettingsViewModel()

    var body: some View {
        VStack {
            FeatureGlassTemplateView(
                icon: "slider.horizontal.3",
                title: viewModel.title,
                subtitle: "这里是 Settings 功能模板，请替换为真实业务 UI"
            )
        }
        .padding()
        .liquidGlassPage()
        .onAppear {
            viewModel.onAppear()
        }
    }
}

struct SettingsView_Previews: PreviewProvider {
    static var previews: some View {
        SettingsView()
    }
}
