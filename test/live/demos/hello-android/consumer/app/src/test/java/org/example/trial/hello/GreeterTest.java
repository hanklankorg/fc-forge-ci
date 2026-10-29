// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: CC0-1.0
package org.example.trial.hello;

import static org.junit.Assert.assertEquals;
import org.junit.Test;

public class GreeterTest {
    @Test
    public void trimsNames() {
        assertEquals("Hello, Forge CI!", Greeter.greet(" Forge CI "));
    }

    @Test
    public void handlesMissingNames() {
        assertEquals("Hello, world!", Greeter.greet("  "));
        assertEquals("Hello, world!", Greeter.greet(null));
    }
}
