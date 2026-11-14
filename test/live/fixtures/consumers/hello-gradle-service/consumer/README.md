# Hello Gradle Service

A tiny Gradle service, a demo for the live tests: a jar and the
container image that runs it.

`Server` answers `GET /` with a greeting and `GET /health` with `ok`, on
port 8080. The build leaves one executable jar in `build/libs`, and the
`Containerfile` copies it into a Java runtime image, so the release ships
the jar as a file and the same jar as an image. A development snapshot
builds the jar the same way and lays it where the release does.

The wrapper pins Gradle and its distribution checksum; the release bumps
the version in `gradle.properties`.

Its workflows name Forge CI where it is published, at a placeholder commit;
`just demo` puts the Forge CI under test in its place.
