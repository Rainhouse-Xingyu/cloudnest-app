import Foundation

/// FRP 页面展示用设备状态
struct FRPDeviceStatusItem: Identifiable {
    let id: Int64
    let deviceName: String
    let deviceIP: String
    let enabled: Bool
    let online: Bool
    let lastHeartbeat: String
    let clientVersion: String
}

/// FRP 映射展示模型
struct FRPMappingItem: Identifiable {
    let id: Int64
    let deviceID: Int64
    let localPort: Int
    let remotePort: Int
    let domain: String
    let enabled: Bool
}

/// 新建设备表单
struct FRPCreateDeviceInput {
    var deviceName: String = ""
    var deviceIP: String = ""
}

/// 新建映射表单
struct FRPCreateMappingInput {
    var deviceID: Int64?
    var localPort: String = ""
    var remotePort: String = ""
    var domain: String = ""
}

/// 新增 FRPC 配置表单
struct FRPConfigFormInput {
    var deviceID: Int64?
    var serverAddr: String = ""
    var serverPort: String = "7000"
    var token: String = ""
}

/// 本地生成的 frpc.yaml 结果
struct FRPYamlGenerateResult {
    let deviceID: Int64
    let localFileURL: URL
}
