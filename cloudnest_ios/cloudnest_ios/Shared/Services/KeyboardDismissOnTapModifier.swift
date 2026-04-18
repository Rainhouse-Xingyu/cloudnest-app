import SwiftUI
import UIKit

/// 全局键盘收起修饰器：点击非键盘区域时结束当前输入焦点。
private struct KeyboardDismissOnTapModifier: ViewModifier {
    func body(content: Content) -> some View {
        content
            // 使用 simultaneousGesture 避免吞掉原有按钮、列表等点击事件。
            .simultaneousGesture(
                TapGesture().onEnded {
                    UIApplication.shared.endEditing()
                }
            )
    }
}

extension View {
    /// 开启“点击空白区域收起键盘”的全局交互。
    func enableGlobalKeyboardDismissOnTap() -> some View {
        modifier(KeyboardDismissOnTapModifier())
    }
}

private extension UIApplication {
    func endEditing() {
        sendAction(#selector(UIResponder.resignFirstResponder), to: nil, from: nil, for: nil)
    }
}
