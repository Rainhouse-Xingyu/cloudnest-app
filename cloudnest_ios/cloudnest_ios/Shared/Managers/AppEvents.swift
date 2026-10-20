import Foundation

/// App 内部事件定义
extension Notification.Name {
    static let authLoginSuccess = Notification.Name("authLoginSuccess")
    static let authLogout = Notification.Name("authLogout")
    static let switchTabToFiles = Notification.Name("switchTabToFiles")
    static let switchTabToTransfers = Notification.Name("switchTabToTransfers")
}
