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

/// Liquid Glass 页面背景：渐变 + 柔和光斑
struct LiquidGlassBackground: View {
    var body: some View {
        ZStack {
            LinearGradient(
                colors: [
                    Color(red: 0.88, green: 0.95, blue: 1.0),
                    Color(red: 0.80, green: 0.92, blue: 1.0),
                    Color(red: 0.94, green: 0.97, blue: 1.0)
                ],
                startPoint: .topLeading,
                endPoint: .bottomTrailing
            )
            .ignoresSafeArea()

            Circle()
                .fill(Color.white.opacity(0.30))
                .frame(width: 280, height: 280)
                .blur(radius: 8)
                .offset(x: -120, y: -260)

            Circle()
                .fill(Color.cyan.opacity(0.18))
                .frame(width: 240, height: 240)
                .blur(radius: 10)
                .offset(x: 140, y: -180)

            Circle()
                .fill(Color.blue.opacity(0.16))
                .frame(width: 260, height: 260)
                .blur(radius: 16)
                .offset(x: 120, y: 300)
        }
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
