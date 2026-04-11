//
//  ContentView.swift
//  cloudnest_ios
//
//  Created by Xingyu Zhao on 2026/4/3.
//

import SwiftUI

struct ContentView: View {
    var body: some View {
        TabView {
            NavigationStack {
                HomeView()
            }
            .tabItem {
                Label("首页", systemImage: "house.fill")
            }

            NavigationStack {
                FilesView()
            }
            .tabItem {
                Label("文件", systemImage: "folder.fill")
            }
            NavigationStack {
                SettingsView()
            }
            .tabItem {
                Label("设置", systemImage: "gearshape.fill")
            }
        }
        .font(.appBody)
        .tint(.blue)
        .liquidGlassPage()
    }
}

#Preview {
    ContentView()
}
