import SwiftUI

/// 登录页面：对接后端 /api/auth/login
struct AuthView: View {
    @StateObject private var viewModel = AuthViewModel()
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        ScrollView {
            VStack(spacing: 18) {
                Image(systemName: "person.crop.circle.badge.checkmark")
                    .font(.system(size: 44, weight: .semibold))
                    .symbolRenderingMode(.hierarchical)
                    .foregroundStyle(.blue)

                Text("CloudNest 登录")
                    .font(.appLargeTitle)

                loginFormCard

                if let errorMessage = viewModel.errorMessage {
                    messageRow(icon: "exclamationmark.triangle.fill", message: errorMessage, color: .red)
                }
            }
            .padding()
        }
        .liquidGlassPage()
        .navigationTitle("认证")
        .navigationBarTitleDisplayMode(.inline)
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
                            .progressViewStyle(.circular)
                            .tint(.white)
                    } else {
                        Image(systemName: "arrow.right.circle.fill")
                    }
                    Text(viewModel.isLoading ? "登录中..." : "登录")
                        .font(.appBody)
                        .fontWeight(.semibold)
                }
                .foregroundStyle(.white)
                .frame(maxWidth: .infinity)
                .padding(.vertical, 12)
                .background(Color.blue, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
            }
            .disabled(!viewModel.canSubmit)
            .opacity(viewModel.canSubmit ? 1.0 : 0.6)
        }
        .liquidGlassCard()
    }

    private func inputRow(title: String, icon: String, text: Binding<String>, isSecure: Bool) -> some View {
        HStack(spacing: 10) {
            Image(systemName: icon)
                .foregroundStyle(.secondary)

            if isSecure {
                SecureField(title, text: text)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled(true)
            } else {
                TextField(title, text: text)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled(true)
            }
        }
        .font(.appBody)
        .padding(.horizontal, 14)
        .padding(.vertical, 12)
        .background(Color.white.opacity(0.42), in: RoundedRectangle(cornerRadius: 12, style: .continuous))
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
