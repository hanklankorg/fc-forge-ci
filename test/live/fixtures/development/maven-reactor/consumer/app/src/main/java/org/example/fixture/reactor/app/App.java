// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package org.example.fixture.reactor.app;

import org.example.fixture.reactor.library.Greeting;

/** The reactor's application module, which needs its sibling library to run. */
public final class App {
  private App() {}

  /**
   * Asks the library for the greeting.
   *
   * @return the library's greeting of the reactor
   */
  public static String greeting() {
    return Greeting.to("reactor");
  }

  /**
   * Prints the greeting.
   *
   * @param args unused
   */
  public static void main(String[] args) {
    System.out.println(greeting());
  }
}
