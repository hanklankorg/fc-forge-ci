# Real Maven library: Forgejo stable signed-file acceptance

Run inside the existing Forge Lab session using the engine's current focused
driver. The existing `LAB_TARGETS_FILE`, runner configuration and
`FORGE_CI_LIVE_SIGNER_IMAGE` apply. No separate SDK, host Maven execution or
project signing configuration is needed.

Set `FORGE_CI_LIVE_CONSUMER` to the absolute clean original library checkout.
Set `FORGE_CI_LIVE_CONSUMER_SETTINGS` to newline-delimited settings:

```text
version=2.7.0
MAVEN_LIBRARY_IMAGE=<runtime Maven/JDK21 image pinned with @sha256:...>
MAVEN_LIBRARY_PROFILE=central-release
RELEASE_PLATFORMS=linux/amd64
```

Then run the existing driver from the engine checkout:

```bash
FORGE_CI_LIVE_CONSUMER_VISIBILITY=public \
FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR=/absolute/external/evidence \
bash scripts/ci/run-live-tests.sh focused '^TestForgejoMavenLibraryConsumer_Stable$'
```

Choose an absolute evidence directory outside both the engine and consumer
checkouts. The shared harness creates missing directories privately and retains
each failed runner job's complete log (up to 8 MiB) before scratch teardown.
Target credentials and supplied fixture secrets are redacted before writing;
the console keeps its 4,000-byte tail and prints the retained file's path.
`FORGE_CI_LIVE_SNAPSHOT_EVIDENCE` remains the fallback environment variable.

The filter runs one public scratch lane through the ordinary focused profile.
Set `FORGE_CI_LIVE_CONSUMER_VISIBILITY=public` in the operator environment.
Both engine and consumer scratch repositories are public; the local staging
index remains private. It admits the clean source through
`OpenConsumer`, overlays native CI and changelog files only in its private Git
index, and pushes a parentless fixture commit. The source POM, Java, quality
rules and test gates remain the admitted originals. `PlanConsumer` rebinds every
native action/workflow reference to the engine under test. The existing trial
helpers establish disposable requester, SSH, GPG and Cosign authority. Maven runs
only in the runner's unsigned producer job, using `build-type: lib` and the
project-owned profile; prepare performs the normal Maven version bump in the
scratch release commit. Signer keys remain in the native isolated signing jobs.
Artifact identity is read from the admitted revision's regular Git POM blob
(mode 100644 or 100755), never from a symlink-following worktree read.

The verdict requires the successful native release workflow, signed release Git
history, verified Cosign signatures binding all four nonempty downloaded POM,
main JAR, sources JAR and Javadoc JAR assets, matching release POM version,
Java 21 classfile major version 65, Java sources and generated Javadoc content.
The current-driver lock and normal scratch cleanup/retention rules apply.

This verdict is a Forgejo signed **file release**, not Maven Central or package
registry acceptance. Fresh registry/classifier resolution, the consumer MAC0
oracle, an independent dev lane, PR acceptance and provisional Nano findings
remain separate work. `Mac0Smoke.java` is supplied as a consumer-specific oracle
but this stable case does not execute it.
