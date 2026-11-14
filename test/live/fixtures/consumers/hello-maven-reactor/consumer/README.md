# Hello Maven Reactor

A tiny multi-module Maven project, a demo for the live tests: a
parent POM, the `greeter` library module and the `app` module that uses it.

The declaration names the parent POM once. One build at the root tests and
packages both modules, one version bump moves the parent and its children,
and the release ships both jars with the aggregate Build SBOM of the reactor.

The application is an executable jar without a container image: its
manifest names `Main` and the greeter jar as its class path, so with the two
release files in one directory `java -jar hello-app-<version>.jar Ada` prints
`Hello, Ada!`. `run-app.sh` does exactly that after the build, in every
forge's gate. The unit tests are the library's: the application module has
none and no dependency outside the reactor, because the vulnerability scan
cannot resolve a module that has both a reactor sibling and an external
dependency.

Its workflows name Forge CI where it is published, at a placeholder commit;
`just demo` puts the Forge CI under test in its place.
