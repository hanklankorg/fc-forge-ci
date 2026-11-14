<!-- SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government -->
<!-- SPDX-License-Identifier: CC0-1.0 -->

# Maven reactor development fixture

The `consumer/` directory is a complete Maven reactor for
`TestForgejoMavenReactorConsumer_Development`: a parent POM (`packaging` pom)
with two JAR modules that share its `0.1.0-SNAPSHOT` version.

| Module | Coordinate | Published files |
|---|---|---|
| `.` | `org.example.fixture.reactor:parent` | POM only |
| `library` | `org.example.fixture.reactor:library` | POM, JAR, sources, Javadoc |
| `app` | `org.example.fixture.reactor:app` | POM, JAR, sources, Javadoc |

`app` depends on `library` at `${project.version}`. Both name the parent, so
a consumer of `app` alone must resolve `library` and the parent POM from the
registry. The parent's `central-release` profile attaches each module's
sources and Javadoc; the fixture targets Java 21 and has no dependency
outside the reactor.

It is a development fixture, not a demo: `just demo` does not show it, and it
carries no CI files. The development fixture of
[`mavenlibrarydevelopment.md`](../../../internal/livetest/mavenlibrarydevelopment.md)
adds the workflow and the registry consumer in its private index, and admits
this shape alone.

## Live provider execution

Read [the live-run instructions](../../../../../docs/live-runs.md) before
scheduling provider work. Copy `consumer/` to a fresh disposable directory,
initialize a Git repository there and commit its files. Set
`FORGE_CI_LIVE_CONSUMER` to that clean checkout's absolute path, as the other
development scenarios do, and name the reactor's oracle:

```bash
export FORGE_CI_LIVE_CONSUMER=/absolute/reactor-checkout
export FORGE_CI_LIVE_CONSUMER_VISIBILITY=public
export FORGE_CI_LIVE_MAVEN_LIBRARY_IMAGE='<runtime Maven/JDK21 image@sha256:digest>'
export FORGE_CI_LIVE_MAVEN_LIBRARY_PROFILE=central-release
export FORGE_CI_LIVE_MAVEN_LIBRARY_ORACLE=/absolute/engine/test/live/fixtures/oracles/maven-reactor/ReactorSmoke.java

bash scripts/ci/run-live-tests.sh focused '^TestForgejoMavenReactorConsumer_Development$'
```

[`ReactorSmoke.java`](../../oracles/maven-reactor/ReactorSmoke.java) is
compiled against the downloaded `app` and fails unless `app` and `library`
were resolved as separate JARs and the application's greeting comes from the
library.

## Evidence

The offline tests check the POM shape, the rendered workflow and that the
product's `--from-build` publisher admits the handoff layout over placeholder
bytes; they run no Maven. Compilation, the attached sources and Javadoc, the
registry's timestamped metadata and the fresh consumer's resolution are
checked only by the live run, which
[`mavenlibrarydevelopment.md`](../../../internal/livetest/mavenlibrarydevelopment.md)
describes.

## One run at a time for an owner

The fixture's coordinates are fixed and a Forgejo package registry belongs to
the owner, not the repository. The test refuses to start when any of the
three snapshot versions already exists, and deletes what it published. So run
it once at a time for an owner; a run that was killed before its cleanup leaves
a snapshot that the next run names in its refusal, to be deleted by hand.
