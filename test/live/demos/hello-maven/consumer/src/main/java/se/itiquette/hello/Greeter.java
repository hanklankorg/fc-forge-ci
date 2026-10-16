package se.itiquette.hello;

/** Says hello. */
public final class Greeter {
  private Greeter() {}

  /** Returns a greeting for the world. */
  public static String greet() {
    return greet("world");
  }

  /** Returns a greeting for the name given, or for the world when it is blank. */
  public static String greet(String name) {
    String who = name == null || name.isBlank() ? "world" : name.strip();
    return "Hello, " + who + "!";
  }
}
