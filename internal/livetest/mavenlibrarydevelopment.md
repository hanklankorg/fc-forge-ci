# Real library development: Forgejo snapshot registry

In the prepared Forge Lab session, use the current focused live driver:

```bash
export FORGE_CI_LIVE_CONSUMER=/absolute/consumer
export FORGE_CI_LIVE_CONSUMER_VISIBILITY=public
export FORGE_CI_LIVE_MAVEN_LIBRARY_IMAGE='<runtime Maven/JDK21 image@sha256:digest>'
export FORGE_CI_LIVE_MAVEN_LIBRARY_PROFILE=central-release
export FORGE_CI_LIVE_MAVEN_LIBRARY_ORACLE=/absolute/engine/test/live/consumer-fixtures/maven-library/Mac0Smoke.java

GO=/absolute/go-1.27.3/bin/go GOROOT=/absolute/go-1.27.3 \
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off \
bash scripts/ci/run-live-tests.sh focused '^TestForgejoMavenLibraryConsumer_Development$'
```

The case creates public scratch repositories on the configured Forgejo account
and runs on its native AMD64 runner. It reads the original clean source and
literal SNAPSHOT coordinates through regular Git POM blobs. Original Maven
checks and the POM are unchanged. Only development CI and an independent
registry-consumer oracle are added to the private local index.

Three native jobs run on a `development` branch, independently of main/stable:

1. Native `build-maven` with `build-type: lib` and the project-owned profile;
   the unsigned original POM/main/sources/Javadoc bytes form an exact-SHA artifact.
2. The existing Go `publish forge-packages deploy --project-type maven
   --from-build --snapshot` path publishes those bytes to the scratch Forgejo
   account's Maven registry. Only this job receives the package token. No signer
   is required and no signing keys enter the producer.
3. A separate runner job creates a fresh Maven local repository, resolves the
   real snapshot POM, transitive dependencies and all three JAR classifiers,
   compares all four downloaded artifacts against the original producer bytes,
   and compiles/runs the supplied Java oracle using only registry dependencies.

The controller additionally requires timestamped snapshot metadata and nonempty
registry files for POM/main/sources/Javadoc, an exact development-SHA success
marker, unchanged scratch main, and no tags/releases. Existing snapshot
coordinates in the disposable owner are refused rather than overwritten;
normal cleanup deletes the newly published snapshot version. There is no
container build/promotion or moving-alias operation in this workflow.

This is development snapshot registry acceptance, not Maven Central acceptance
or a new stable release. The supplied MAC0 oracle tests the real public API's
correct-key round trip and wrong-key rejection. Other libraries supply their own
regular Java oracle file; generic fixture code carries no library identity.
