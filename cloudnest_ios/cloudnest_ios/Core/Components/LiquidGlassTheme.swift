import SwiftUI

/// 全局字体规范：统一使用 SF Pro（系统默认字体）
extension Font {
    static let appLargeTitle = Font.system(size: 32, weight: .bold, design: .default)
    static let appTitle = Font.system(size: 22, weight: .semibold, design: .default)
    static let appBody = Font.system(size: 16, weight: .regular, design: .default)
    static let appCaption = Font.system(size: 13, weight: .regular, design: .default)
}

/// Liquid Glass 卡片效果：用于承载主要内容块
struct LiquidGlassCardModifier: ViewModifier {
    func body(content: Content) -> some View {
        content
            .padding(20)
            .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 26, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: 26, style: .continuous)
                    .stroke(Color.white.opacity(0.42), lineWidth: 1)
            )
            .shadow(color: Color.black.opacity(0.10), radius: 20, x: 0, y: 10)
    }
}

/// 页面背景：纯白色
struct LiquidGlassBackground: View {
    var body: some View {
        Color.white
            .ignoresSafeArea()
    }
}

extension View {
    /// 应用到整个页面的 Liquid Glass 背景
    func liquidGlassPage() -> some View {
        self.background(LiquidGlassBackground())
    }

    /// 应用到模块内容容器的 Liquid Glass 卡片
    func liquidGlassCard() -> some View {
        self.modifier(LiquidGlassCardModifier())
    }
}
