# Hello Gradle

A tiny Gradle Java library, a demo of the `gradle-app` example for the live
tests: `my-gradle-lib`, with its sources and Javadoc jars, built reproducibly
and tested with JUnit 5. The wrapper pins Gradle and its distribution checksum.

Its CI files are the example's, filled in as an adopter would, and are
rewritten from it rather than edited here; `just demo` puts the Forge CI
under test in place of the pinned one. The GitLab file is the merge-request
gate: the lint gate, `./gradlew check`, and the `build-gradle` component.
The GitLab release scripts in `.gitlab/` are the demo's own, since the
example ships none: the release policy's checks run `./gradlew check`, and
its java build job `forge-ci build gradle run`.
