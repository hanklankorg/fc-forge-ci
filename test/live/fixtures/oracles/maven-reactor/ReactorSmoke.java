// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: CC0-1.0

import org.example.fixture.reactor.app.App;
import org.example.fixture.reactor.library.Greeting;

/** Reactor oracle, compiled against the downloaded application module only. */
public final class ReactorSmoke {
  public static void main(String[] args) throws Exception {
    // The consumer declares the application alone; the library is on the
    // class path only if Maven resolved it, and the parent POM, transitively.
    String app = App.class.getProtectionDomain().getCodeSource().getLocation().toString();
    String library = Greeting.class.getProtectionDomain().getCodeSource().getLocation().toString();
    if (!app.endsWith(".jar") || !library.endsWith(".jar") || app.equals(library)) {
      throw new IllegalStateException("modules were not resolved as separate JARs: " + app + " " + library);
    }
    if (!"Hello, reactor, from the library".equals(App.greeting())) {
      throw new IllegalStateException("application did not run against its library: " + App.greeting());
    }
    App.main(args);
    System.out.println("Maven reactor application ran against its registry-resolved library");
  }
}
