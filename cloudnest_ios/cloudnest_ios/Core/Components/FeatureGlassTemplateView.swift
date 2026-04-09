import SwiftUI

/// 统一的模块模板页面：图标使用 SF Symbols，样式使用 Liquid Glass
struct FeatureGlassTemplateView: View {
    let icon: String
    let title: String
    let subtitle: String

    var body: some View {
        VStack(spacing: 20) {
            Image(systemName: icon)
                .font(.system(size: 34, weight: .semibold, design: .default))
                .symbolRenderingMode(.hierarchical)
                .foregroundStyle(Color.blue)

            Text(title)
                .font(.appTitle)
                .foregroundStyle(Color.primary)

            Text(subtitle)
                .font(.appCaption)
                .foregroundStyle(Color.secondary)
                .multilineTextAlignment(.center)
        }
        .frame(maxWidth: .infinity)
        .liquidGlassCard()
    }
}
