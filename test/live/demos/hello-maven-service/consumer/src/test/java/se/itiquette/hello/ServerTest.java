package se.itiquette.hello;

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
