// Compiled only by scripts/build-macos-native-ui.py, outside the shipping target.
import SwiftUI
import Security

private struct NativeUIFixture: Decodable {
    let certificate: String
    let directory: String
}

// UI fixtures never persist credentials or request access to another build's
// Keychain items. The shipping vault is covered by dedicated Keychain tests.
private final class NativeUISessionVault: SessionVault {
    private var session: SavedSession?
    func read() throws -> SavedSession? { session }
    func write(_ session: SavedSession) throws { self.session = session }
    func clear() throws { session = nil }
}

private final class NativeUITLSPolicy: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
    let certificate: SecCertificate

    init(der: Data) throws {
        guard let certificate = SecCertificateCreateWithData(nil, der as CFData) else { throw ServerError.invalidResponse }
        self.certificate = certificate
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didReceive challenge: URLAuthenticationChallenge,
                    completionHandler: @escaping @Sendable (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
              let trust = challenge.protectionSpace.serverTrust,
              SecTrustSetAnchorCertificates(trust, [certificate] as CFArray) == errSecSuccess,
              SecTrustSetAnchorCertificatesOnly(trust, true) == errSecSuccess,
              SecTrustEvaluateWithError(trust, nil) else {
            completionHandler(.cancelAuthenticationChallenge, nil)
            return
        }
        completionHandler(.useCredential, URLCredential(trust: trust))
    }

    func urlSession(_ session: URLSession, task: URLSessionTask,
                    willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest) async -> URLRequest? { nil }
}

@main
struct CloudNestNativeUI: App {
    @State private var session: SessionState
    @State private var transfers: TransferManager

    init() {
        do {
            guard let path = Bundle.main.object(forInfoDictionaryKey: "CloudNestNativeUIManifest") as? String
                    ?? ProcessInfo.processInfo.environment["CLOUDNEST_NATIVE_UI_MANIFEST"] else {
                fatalError("Launch with CLOUDNEST_NATIVE_UI_MANIFEST pointing to the disposable fixture.")
            }
            let fixture = try JSONDecoder().decode(NativeUIFixture.self, from: Data(contentsOf: URL(fileURLWithPath: path)))
            let policy = try NativeUITLSPolicy(der: Data(contentsOf: URL(fileURLWithPath: fixture.certificate)))
            let configuration = URLSessionConfiguration.ephemeral
            configuration.httpCookieStorage = nil
            configuration.urlCredentialStorage = nil
            configuration.urlCache = nil
            let transport = URLSession(configuration: configuration, delegate: policy, delegateQueue: nil)
            let api = ServerAPI(session: transport, delegate: policy)
            let root = URL(fileURLWithPath: fixture.directory)
            let state = SessionState(api: api, vault: NativeUISessionVault())
            _session = State(initialValue: state)
            _transfers = State(initialValue: TransferManager(session: state, directory: root.appending(path: "journal")))
        } catch { fatalError("Could not open the dedicated native UI fixture: \(error)") }
    }

    var body: some Scene {
        WindowGroup("CloudNest Native UI", id: "workspace") {
            WorkspaceWindow(session: session, transfers: transfers)
                .frame(minWidth: 640, minHeight: 440)
                .task { await session.restore(); await transfers.restore() }
        }
        .defaultSize(width: 900, height: 590)
        .onChange(of: session.transferIdentity) { Task { await transfers.synchronizeIdentity() } }
        .commands {
            FileBrowserCommands()
            CommandMenu("服务器") {
                Button("刷新连接状态") { Task { await session.refresh() } }
                Button("退出登录") { Task { await session.signOut() } }
            }
        }
    }
}
