// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: CC0-1.0
import SwiftUI

@main
struct HelloTrialApp: App {
    var body: some Scene {
        WindowGroup {
            Text(Greeter.greet("Forge CI"))
        }
    }
}
