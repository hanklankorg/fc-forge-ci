// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: CC0-1.0
package org.example.fixture.gradleconsumer;

import org.example.fixture.gradlelibrary.Greeting;

/** Runs the resolved library's code and prints what it returned. */
public final class Smoke {
  private Smoke() {}

  public static void main(String[] args) {
    System.out.println(Greeting.greet("fresh consumer"));
  }
}
