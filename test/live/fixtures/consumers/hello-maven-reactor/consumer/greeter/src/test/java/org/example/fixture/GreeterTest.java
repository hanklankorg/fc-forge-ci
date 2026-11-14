// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package org.example.fixture;

import static org.junit.jupiter.api.Assertions.assertEquals;

import org.junit.jupiter.api.Test;

class GreeterTest {
  @Test
  void greetsTheWorld() {
    assertEquals("Hello, world!", Greeter.greet());
  }

  @Test
  void greetsByName() {
    assertEquals("Hello, Ada!", Greeter.greet(" Ada "));
  }

  @Test
  void greetsTheWorldForABlankName() {
    assertEquals("Hello, world!", Greeter.greet("  "));
  }
}
