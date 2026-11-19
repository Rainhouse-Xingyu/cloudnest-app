// swift-tools-version: 6.2
import PackageDescription

let package = Package(
    name: "CloudNestCore",
    platforms: [.macOS(.v26)],
    targets: [
        .target(name: "CloudNestCore", path: "Core"),
        .testTarget(name: "CloudNestCoreTests", dependencies: ["CloudNestCore"], path: "Tests")
    ]
)
