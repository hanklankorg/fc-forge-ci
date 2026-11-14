// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: CC0-1.0

// SwiftPM manifest for CodeQL analysis. The actual demo builds with
// XcodeGen (project.yml) + xcodebuild on macOS. This Package.swift exists
// so CodeQL's Swift autobuilder can compile the sources and extract a
// database. It is not used by the demo's own build or test workflow.

// swift-tools-version:6.0
import PackageDescription

let package = Package(
    name: "HelloTrial",
    platforms: [.macOS(.v14)],
    targets: [
        .executableTarget(
            name: "HelloTrial",
            path: "Sources"
        ),
        .testTarget(
            name: "HelloTrialTests",
            dependencies: ["HelloTrial"],
            path: "Tests"
        ),
    ]
)
