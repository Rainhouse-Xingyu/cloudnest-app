import SwiftUI

/// 登录页面：对接后端 /api/auth/login
struct AuthView: View {
    @StateObject private var viewModel = AuthViewModel()
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        ScrollView(showsIndicators: false) {
            VStack(spacing: 32) {
                Spacer().frame(height: 60)
                
                // Header Hero
                VStack(spacing: 12) {
                    Image(systemName: "cloud.fill")
                        .font(.system(size: 64, weight: .semibold))
                        .foregroundStyle(
                            LinearGradient(
                                colors: [Color.blue.opacity(0.8), Color.cyan.opacity(0.8)],
                                startPoint: .topLeading,
                                endPoint: .bottomTrailing
                            )
                        )
                        .shadow(color: Color.blue.opacity(0.3), radius: 10, x: 0, y: 6)

                    Text("Rainhouse")
                        .font(.system(size: 38, weight: .bold, design: .rounded))
                        .foregroundStyle(.primary)
                    
                    Text("Your Data,Your Home")
                        .font(.appBody)
                        .foregroundStyle(.secondary)
                        .tracking(1.2)
                }

                loginFormCard

                if let errorMessage = viewModel.errorMessage {
                    messageRow(icon: "exclamationmark.triangle.fill", message: errorMessage, color: .red)
                        .transition(.opacity.combined(with: .move(edge: .top)))
                }
            }
            .padding(.horizontal, 24)
        }
        .liquidGlassPage()
        .navigationBarHidden(true)
        .onAppear {
            viewModel.onAppear()
        }
    }

    private var loginFormCard: some View {
        VStack(spacing: 14) {
            inputRow(title: "用户名", icon: "person.fill", text: $viewModel.username, isSecure: false)
            inputRow(title: "密码", icon: "lock.fill", text: $viewModel.password, isSecure: true)

            // 外网连接开关：勾选后展示 IP 输入框
            Button {
                viewModel.useExternalConnection.toggle()
            } label: {
                HStack(spacing: 10) {
                    Image(systemName: viewModel.useExternalConnection ? "checkmark.circle.fill" : "circle")
                        .font(.system(size: 20, weight: .semibold))
                        .foregroundStyle(viewModel.useExternalConnection ? Color.blue : Color.secondary)

                    Text("外网连接")
                        .font(.appBody)
                        .foregroundStyle(.primary)

                    Spacer()
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)

            if viewModel.useExternalConnection {
                inputRow(
                    title: "请输入 IP 地址（可带端口）",
                    icon: "network",
                    text: $viewModel.externalIP,
                    isSecure: false
                )
                .keyboardType(.URL)
            }

            Button {
                Task {
                    let success = await viewModel.login()
                    guard success else { return }

                    // 登录成功后先切回首页，再关闭当前页面。
                    NotificationCenter.default.post(name: .authLoginSuccess, object: nil)
                    viewModel.password = ""
                    dismiss()
                }
            } label: {
                HStack(spacing: 8) {
                    if viewModel.isLoading {
                        ProgressView()
                            .tint(.white)
                    } else {
                        Image(systemName: "arrow.right")
                            .font(.system(size: 18, weight: .bold))
                    }
                    Text(viewModel.isLoading ? "登录中..." : "开启探索")
                        .font(.system(size: 17, weight: .bold, design: .rounded))
                }
                .foregroundStyle(.white)
                .frame(maxWidth: .infinity)
                .padding(.vertical, 16)
                .background(
                    LinearGradient(
                        colors: viewModel.canSubmit ? [Color.blue, Color.cyan] : [Color.gray.opacity(0.4), Color.gray.opacity(0.3)],
                        startPoint: .topLeading,
                        endPoint: .bottomTrailing
                    )
                    .clipShape(RoundedRectangle(cornerRadius: 16, style: .continuous))
                )
                .shadow(color: viewModel.canSubmit ? Color.blue.opacity(0.4) : .clear, radius: 10, x: 0, y: 6)
            }
            .buttonStyle(.plain)
            .disabled(!viewModel.canSubmit)
            .animation(.spring(), value: viewModel.canSubmit)
        }
        .liquidGlassCard()
    }

    private func inputRow(title: String, icon: String, text: Binding<String>, isSecure: Bool) -> some View {
        HStack(spacing: 12) {
            Image(systemName: icon)
                .foregroundStyle(.blue.opacity(0.8))
                .font(.system(size: 18, weight: .medium))
                .frame(width: 24)

            Group {
                if isSecure {
                    SecureField(title, text: text)
                } else {
                    TextField(title, text: text)
                }
            }
            .textInputAutocapitalization(.never)
            .autocorrectionDisabled(true)
            .font(.system(size: 16, weight: .medium, design: .rounded))
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 16)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 16, style: .continuous)
                .stroke(Color.white.opacity(0.6), lineWidth: 1)
        )
        .shadow(color: Color.blue.opacity(0.04), radius: 8, x: 0, y: 4)
    }

    private func messageRow(icon: String, message: String, color: Color) -> some View {
        HStack(spacing: 8) {
            Image(systemName: icon)
                .foregroundStyle(color)
            Text(message)
                .font(.appCaption)
                .foregroundStyle(color)
            Spacer()
        }
        .padding(10)
        .background(color.opacity(0.10), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
    }
}

struct AuthView_Previews: PreviewProvider {
    static var previews: some View {
        AuthView()
    }
}
