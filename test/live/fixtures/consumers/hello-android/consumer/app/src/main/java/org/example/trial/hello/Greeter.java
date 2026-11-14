// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later
package org.example.trial.hello;

public final class Greeter {
    private Greeter() {}

    public static String greet(String name) {
        String trimmed = name == null ? "" : name.trim();
        return "Hello, " + (trimmed.isEmpty() ? "world" : trimmed) + "!";
    }
}
