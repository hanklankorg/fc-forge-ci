// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later
package org.example.trial.hello.library;

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
