// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: CC0-1.0
import Foundation

enum Greeter {
    static func greet(_ name: String) -> String {
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        return "Hello, \(trimmed.isEmpty ? "world" : trimmed)!"
    }
}
