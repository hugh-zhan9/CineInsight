// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "CineInsightNative",
    platforms: [
        .macOS(.v14)
    ],
    products: [
        .library(name: "CineInsightNativeCore", targets: ["CineInsightNativeCore"]),
        .executable(name: "CineInsightNative", targets: ["CineInsightNative"]),
        .executable(name: "CineInsightNativeSmokeTests", targets: ["CineInsightNativeSmokeTests"])
    ],
    targets: [
        .target(
            name: "CineInsightNativeCore"
        ),
        .executableTarget(
            name: "CineInsightNative",
            dependencies: ["CineInsightNativeCore"]
        ),
        .executableTarget(
            name: "CineInsightNativeSmokeTests",
            dependencies: ["CineInsightNativeCore"]
        )
    ]
)
