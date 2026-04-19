//
//  ContentView.swift
//  cloudnest_ios
//
//  Created by Xingyu Zhao on 2026/4/3.
//

import SwiftUI

struct ContentView: View {
    @State private var showAuth: Bool = false
    @State private var selectedTab: Int = 0
    
    var body: some View {
        TabView(selection: $selectedTab) {
            NavigationStack {
                HomeView()
            }
            .tabItem {
                Label("首页", systemImage: "house.fill")
            }
            .tag(0)

            NavigationStack {
                FilesView()
            }
            .tabItem {
                Label("文件", systemImage: "folder.fill")
            }
            .tag(1)

            NavigationStack {
                PhotosView()
            }
            .tabItem {
                Label("照片", systemImage: "photo.on.rectangle")
            }
            .tag(2)
            
            NavigationStack {
                SettingsView()
            }
            .tabItem {
                Label("设置", systemImage: "gearshape.fill")
            }
            .tag(3)
        }
        .font(.appBody)
        .tint(.blue)
        .enableGlobalKeyboardDismissOnTap()
        .onReceive(NotificationCenter.default.publisher(for: .switchTabToFiles)) { _ in
            selectedTab = 1
        }
        .onReceive(NotificationCenter.default.publisher(for: .switchTabToPhotos)) { _ in
            selectedTab = 2
        }
        .liquidGlassPage()
        .onAppear {
            checkAuthStatus()
        }
        .fullScreenCover(isPresented: $showAuth) {
            NavigationStack {
                AuthView()
            }
            .enableGlobalKeyboardDismissOnTap()
        }
        .onReceive(NotificationCenter.default.publisher(for: .authLoginSuccess)) { _ in
            showAuth = false
            selectedTab = 0
        }
        .onReceive(NotificationCenter.default.publisher(for: .authLogout)) { _ in
            showAuth = true
            selectedTab = 0
        }
    }
    
    private func checkAuthStatus() {
        if SessionManager.shared.loadSession() == nil {
            showAuth = true
        }
    }
}

#Preview {
    ContentView()
}
