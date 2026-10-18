package se.itiquette.hello;

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
