// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: CC0-1.0
package org.hanklankorg.trial.hello;

public final class Greeter {
    private Greeter() {}

    public static String greet(String name) {
        String trimmed = name == null ? "" : name.trim();
        return "Hello, " + (trimmed.isEmpty() ? "world" : trimmed) + "!";
    }
}
