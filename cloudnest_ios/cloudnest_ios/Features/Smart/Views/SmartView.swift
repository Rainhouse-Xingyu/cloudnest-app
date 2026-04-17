import SwiftUI

struct SmartView: View {
    var body: some View {
        Text("智能功能开发中")
            .font(.appTitle)
            .navigationTitle("智能")
            .navigationBarTitleDisplayMode(.inline)
            .liquidGlassPage()
    }
}

#Preview {
    NavigationStack {
        SmartView()
    }
}
