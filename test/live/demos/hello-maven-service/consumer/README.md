# Hello Maven Service

A tiny Maven service for showing Forge CI on Forge Lab: a jar and the
container image that runs it.

`Server` answers `GET /` with a greeting and `GET /health` with `ok`, on
port 8080. The `Containerfile` copies the jar Maven builds into a Java
runtime image, so the release ships the jar as a file and the same jar as
an image.

Its workflows name Forge CI where it is published, at a placeholder commit;
`just demo` puts the Forge CI under test in its place.
