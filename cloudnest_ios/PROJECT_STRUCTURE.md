# cloudnest_ios 标准目录（MVVM + Feature 分层）

## 目录结构

```text
cloudnest_ios/
  App/
    Application/        # App 生命周期、启动初始化
    Routing/            # 路由与页面跳转管理
    DependencyInjection/# 依赖注入容器与组装

  Core/
    Network/            # 网络层（请求、响应、拦截、错误映射）
    Storage/            # 本地存储（UserDefaults/Keychain/缓存）
    Extensions/         # 通用扩展
    Utils/              # 通用工具函数
    Components/         # 跨模块可复用 UI 组件
    Constants/          # 全局常量与配置键

  Features/
    Auth/
      Views/            # 页面视图
      ViewModels/       # 业务状态与交互逻辑
      Models/           # Feature 内部数据模型
      Services/         # Feature 相关服务
    Home/
      Views/
      ViewModels/
      Models/
      Services/
    Files/
      Views/
      ViewModels/
      Models/
      Services/
    Profile/
      Views/
      ViewModels/
      Models/
      Services/
    Settings/
      Views/
      ViewModels/
      Models/
      Services/

  Shared/
    Models/             # 多 Feature 共享数据模型
    Services/           # 多 Feature 共享服务
    Managers/           # 跨模块管理器（会话、权限、上传队列等）

  Resources/
    Localizable/        # 多语言资源
    Fonts/              # 字体资源
    Colors/             # 颜色资源（可配合 Asset Catalog）
    LaunchScreen/       # 启动页相关资源

  SupportingFiles/
    Config/             # 配置文件（plist/json 等）
    Environments/       # 环境配置（dev/test/prod）
```

## 使用建议

1. 新功能优先在 `Features/<FeatureName>/` 下闭环开发，避免业务散落在全局目录。
2. 通用能力（网络、缓存、鉴权工具）优先放在 `Core/` 或 `Shared/`，减少重复实现。
3. `View` 仅负责展示与用户事件，业务状态和流程放入 `ViewModel`。
4. 页面路由统一收敛在 `App/Routing/`，便于后期替换导航方案。
5. 环境配置不要硬编码在代码中，放入 `SupportingFiles/Environments/`。

## 后续可选增强

1. 为每个 Feature 增加 `Tests/` 子目录，建立模块级单元测试。
2. 增加 `DesignSystem/`（颜色、字体、间距、组件规范）统一视觉系统。
3. 加入 `Scripts/` 目录集中管理构建、代码检查与资源同步脚本。
