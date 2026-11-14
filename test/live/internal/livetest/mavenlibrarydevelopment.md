# Maven library development: Forgejo snapshot registry

`TestForgejoMavenReactorConsumer_Development` runs the development build of a
Maven library on the shipped
[reactor fixture](../../fixtures/development/maven-reactor/README.md), which
names its inputs and the command. The case creates public scratch
repositories on the configured Forgejo account and runs on its native AMD64
runner. It reads the clean source and its literal SNAPSHOT coordinates
through regular Git POM blobs. The fixture's POMs are unchanged. Only
development CI and the registry-consumer oracle are added to the private
local index.

The reactor must be a SNAPSHOT `pom` aggregator whose direct modules are
literal JAR modules naming it as parent at its version. Exactly one module
has no dependent sibling and depends on a sibling itself; that module is the
coordinate the fresh consumer declares. Any other shape is refused before the
private index changes.

Three native jobs run on a `development` branch, independently of main and
of a release:

1. Native `build-maven` with `build-type: lib` and the project-owned profile.
   The build job hands over each module's original POM, and each JAR module's
   main, sources and Javadoc JARs, as `<module>/pom.xml` and
   `<module>/target/`, in an artifact named after the commit.
2. One `publish forge-packages deploy --project-type maven --from-build
   --snapshot` publishes every module from that handoff to the scratch
   account's Maven registry: the parent as a POM only, each JAR module with
   its three JARs. Only this job receives the package token. No signer is
   required and no signing key enters the producer.
3. A separate runner job creates a fresh Maven local repository and declares
   only the consumed module, so its sibling and the parent POM resolve from
   the registry. Every publication is then read back by coordinate and
   compared with the producer's bytes, and the oracle runs the application
   against the resolved library, using registry dependencies alone.

The controller refuses a pre-existing snapshot of any module and deletes
every module's snapshot afterwards. It requires timestamped snapshot metadata
and nonempty registry files for each publication: POM, main, sources and
Javadoc for a JAR module, and for the parent a POM and no other entry. It
also requires the success marker of the pushed commit, an unchanged scratch
main, and no tags or releases. There is no container build, promotion or
moving-alias operation in this workflow.

This tests a development snapshot in the forge's registry, not Maven Central
and not a stable release.

The offline tests cover the POM contract, the rendered workflow and the
handoff layout the publisher reads; they execute no Maven.
