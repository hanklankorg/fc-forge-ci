// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: CC0-1.0
import XCTest
@testable import HelloTrial

final class GreeterTests: XCTestCase {
    func testGreeting() {
        XCTAssertEqual(Greeter.greet(" Forge CI "), "Hello, Forge CI!")
        XCTAssertEqual(Greeter.greet(" \n"), "Hello, world!")
    }
}
