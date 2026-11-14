// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: CC0-1.0
package org.example.fixture.gradlelibrary;

/** The one class of the published library. */
public final class Greeting {
  private Greeting() {}

  /**
   * Greets a caller.
   *
   * @param name who is greeted
   * @return the greeting only this library's code produces
   */
  public static String greet(String name) {
    return "hello, " + name + ", from hello-gradle-library";
  }
}
