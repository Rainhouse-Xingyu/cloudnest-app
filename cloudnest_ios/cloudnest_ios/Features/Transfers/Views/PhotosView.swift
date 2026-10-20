import SwiftUI

/// 照片模块入口页（4.1 导航结构首版）
struct PhotosView: View {
    var body: some View {
        ScrollView(showsIndicators: false) {
            VStack(spacing: 16) {
                FeatureGlassTemplateView(
                    icon: "photo.on.rectangle.angled",
                    title: "照片",
                    subtitle: "照片模块已接入 Tab Bar，后续将继续完善排序、选择与 Live Photo 详情能力。"
                )
            }
            .padding(.horizontal, 16)
            .padding(.top, 16)
            .padding(.bottom, 24)
        }
        .navigationTitle("照片")
        .liquidGlassPage()
    }
}

#Preview {
    NavigationStack {
        PhotosView()
    }
}