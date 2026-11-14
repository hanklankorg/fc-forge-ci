<!-- SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government -->
<!-- SPDX-License-Identifier: CC0-1.0 -->

# Gradle JVM library fixture

Two Gradle builds for
`TestInRunner_ForgePackages_GradleLibraryReachesAFreshConsumer`:

| Build | What it is |
|---|---|
| `producer/` | `org.example.fixture:hello-gradle-library`: `java-library` and `maven-publish`, one publication of the Java component with a sources JAR, and no repository of its own |
| `consumer/` | An `application` whose only repository is the registry it is given, and whose only dependency is the producer's coordinate |

Neither build declares a dependency or plugin outside Gradle itself. Both
compile for Java 21 with the JDK that runs Gradle.

The scenario commits both builds to a scratch repository. To each it adds the
Gradle wrapper of the [Gradle demo](../../consumers/hello-gradle/consumer/),
so the repository keeps one copy of the wrapper JAR and one pinned
distribution checksum. It writes the run's version to
`producer/gradle.properties`, because the registry belongs to the owner and a
version cannot be published twice.

The job downloads that commit and runs the product's two phases for a Forgejo
consumer ([Gradle publishing](../../../../../docs/publishing.md#gradle-jvm-and-android-libraries)):

```sh
forge-ci build gradle export-publications --working-dir fixture/producer --output publication --version <version>
forge-ci publish forge-packages deploy --project-type gradle --working-dir publication --version <version>
```

It then runs `consumer/` with no token and a Gradle home that holds the
distribution alone. `Smoke` prints what the library's `Greeting` returns.

It is not a demo: `just demo` does not show it, and it carries no CI files.

## Evidence still to produce

Gradle has never built these two builds: they were written without a Gradle
run or a distribution download. The offline tests check the fixture's shape,
the rendered job against stand-ins for the product and the wrapper, and the
registry oracles over placeholder bytes. Compilation, the exported module
metadata, the registry's handling of the checksum sidecars and the `.module`
file, and the fresh consumer's resolution are native evidence only. No native
run is implied by this fixture.
