// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package org.example.fixture;

import static org.junit.jupiter.api.Assertions.assertEquals;

import org.junit.jupiter.api.Test;

class ServerTest {
  @Test
  void rootGreets() {
    assertEquals(new Server.Response(200, "Hello, world!"), Server.respond("/"));
  }

  @Test
  void healthIsOk() {
    assertEquals(new Server.Response(200, "ok"), Server.respond("/health"));
  }

  @Test
  void otherPathsAreNotFound() {
    assertEquals(404, Server.respond("/missing").status());
  }
}
