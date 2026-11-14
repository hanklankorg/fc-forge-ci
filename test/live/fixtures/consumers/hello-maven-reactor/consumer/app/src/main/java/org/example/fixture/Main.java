// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package org.example.fixture;

/** Prints the greeting of the greeter module. */
public final class Main {
  private Main() {}

  /** Returns the line the program prints for its arguments. */
  static String line(String[] args) {
    return args.length == 0 ? Greeter.greet() : Greeter.greet(args[0]);
  }

  /** Greets the first argument, or the world. */
  public static void main(String[] args) {
    System.out.println(line(args));
  }
}
