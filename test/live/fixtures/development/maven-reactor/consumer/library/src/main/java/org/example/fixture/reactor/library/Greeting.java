// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package org.example.fixture.reactor.library;

/** The reactor's library module: the greeting its application module prints. */
public final class Greeting {
  private Greeting() {}

  /**
   * Greets a name.
   *
   * @param name who is greeted
   * @return the greeting
   */
  public static String to(String name) {
    return "Hello, " + name + ", from the library";
  }
}
