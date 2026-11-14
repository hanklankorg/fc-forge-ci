# Configuration Examples

Complete working examples for different project types.

> Each example follows one of the two ecosystem patterns (A: build artefact + container COPY; B: container is the build environment). See **[docs/ecosystems.md](../docs/ecosystems.md)** for the full framing and per-ecosystem capability matrices.
>
> Examples pin `v0.90.0` as a placeholder release. Pin the release you install
> and its runtime image digests instead, as
> **[Which version to pin](../docs/README.md#documentation-index)** describes.
> For an unreleased candidate, use the branch-testing flow in
> **[docs/runtime-images.md](../docs/runtime-images.md)**.
>
> Image digests such as `PIN_DIGEST` are placeholders. Select a published,
> readable lint image for the required toolchain and pin its verified digest.
> See **[lint images](../docs/components.md#lint-nanolinteryml-default)**.

## Available Examples

One directory per ecosystem, shared by all three forges. Each holds the
forge-neutral `artifacts.yml`, a `nanolinter.toml` verify plan for the lint
gate, a README with a short "Per forge" list, and a subdirectory per forge
that builds it: `github/` with the caller workflows,
`gitlab/.gitlab-ci.yml`, and `forgejo/pullrequest.yml` with
`forgejo/release.yml` and, where offered, `forgejo/snapshot.yml`. A forge
without a directory cannot build that ecosystem; [Delivery layers side by side](../docs/providers.md#delivery-layers-side-by-side)
has the capability table.

| Ecosystem | GitHub | GitLab | Forgejo |
|---|---|---|---|
| [Go CLI](go-cli/): standalone binaries, cross-compiled per platform | [workflows](go-cli/github/) | [pipeline](go-cli/gitlab/.gitlab-ci.yml) | [workflows](go-cli/forgejo/) |
| [Go Service](go-service/): a multi-arch container, compiled in the Containerfile (Pattern B) | [workflows](go-service/github/) | [pipeline](go-service/gitlab/.gitlab-ci.yml) | [workflows](go-service/forgejo/) |
| [Rust Application](cargo-app/): a Cargo workspace shipped as containers, with the private-registry build-secrets pattern | [workflows](cargo-app/github/) | [pipeline](cargo-app/gitlab/.gitlab-ci.yml) | [workflows](cargo-app/forgejo/) |
| [Maven Application](maven-app/): a Java service shipped as a multi-platform container (Pattern A) | [workflows](maven-app/github/) | [pipeline](maven-app/gitlab/.gitlab-ci.yml) | [workflows](maven-app/forgejo/) |
| [Gradle JVM](gradle-app/): JVM builds and [artifact-first package publication](../docs/publishing.md#gradle-jvm-and-android-libraries) | [workflows](gradle-app/github/) | [pipeline](gradle-app/gitlab/.gitlab-ci.yml); publication requires explicit artifact handoff | [workflows](gradle-app/forgejo/) |
| [NPM Application](npm-app/): a Node service published as a package and shipped as a container (Pattern A) | [workflows](npm-app/github/) | [pipeline](npm-app/gitlab/.gitlab-ci.yml) | not available |
| [Android Application](android-app/): APK and AAB with product flavors, optional Google Play upload; a [library variant](android-app/README.md#library-variant) builds an AAR | [workflows](android-app/github/) | [pipeline](android-app/gitlab/.gitlab-ci.yml) | not available |
| [Monorepo](monorepo/): several artifacts and project types, one container per artifact or one combined | [workflows](monorepo/github/) | [pipeline](monorepo/gitlab/.gitlab-ci.yml) | [workflows](monorepo/forgejo/) |
| [No build](no-build/): scripts and configuration distributed through Git, `project-type: meta` | [workflows](no-build/github/) | [pipeline](no-build/gitlab/.gitlab-ci.yml) | [workflows](no-build/forgejo/) |

What each forge's files do differs with its layer. The GitHub workflows call
the orchestrators and read `artifacts.yml`. The GitLab pipelines are the
merge-request gate and build, from the CI/CD components; a release runs from
a separate policy project, as the [GitLab guide](../docs/gitlab.md#releases-and-promotion)
describes. The Forgejo pair puts your build job between the layer's prepare
and signer workflows; it reads no `artifacts.yml`, because the workflow is the
plan. The [Forgejo signer-image workflow](forgejo/) is for the organization
that serves the layer, not for a consumer. The
[runner lender](runner-lender/github-actions/lend-runner.yml) is for an
installation whose Forgejo runner pool denies the user namespaces rootless
Buildah needs: a public GitHub repository lends a VM for one container-build
job at a time; see [Runners and runner lending](../docs/runners.md).

### Container snapshots: [`container-snapshot/`](container-snapshot/)

Development images of any branch, built from a container declaration alone,
with the scan and SBOM settings of the declaration. Publishing is on by default;
`publish-containers: false` turns it off.

### Base images: [`base-images/`](base-images/)

A root image and two flavors built on it, each signed and promoted. For
Forgejo and GitLab.

### By signing backend: [`signing/`](signing/)

How a GitHub release is signed, independent of what is being released.
Combine one with any ecosystem example above: what each adds is the `sign:`
(and `git-signing:`) block of its `artifacts.yml` and the `secrets:` lines of
its `github/release-workflow.yml`; its README says where they go. The rest of
both files is a placeholder project: the Go CLI, or a Maven library for
OpenBao.
The GitLab and Forgejo layers sign with a Cosign key pair of the organization;
see their guides.

- **[Sigstore keyless](signing/sigstore-keyless/)**: the recommended
  default on a keyless-capable forge: no secret management and no key on
  the runner. See [Verification](../docs/verification.md).
- **[OpenBao KMS](signing/openbao-kms/)**: for deployments where the trust
  anchor has to stay inside your own infrastructure. With transparency
  disabled, signing avoids public Sigstore endpoints but still reaches OpenBao
  and the normal release services.
- **[Cosign key](signing/cosign-key/)**: a Cosign key pair held as secrets,
  the way the GitLab and Forgejo layers sign, so one key serves every forge.

---

## Quick Start

### Using an Example

1. **Pick the ecosystem directory** and your forge's subdirectory in it.

2. **Copy files to your project:**
   ```bash
   # The artifacts configuration, when your forge reads one
   cp examples/maven-app/artifacts.yml /path/to/your/project/.forge-ci/
   # The lint plan every forge's pull-request gate runs
   cp examples/maven-app/nanolinter.toml /path/to/your/project/

   # GitHub
   cp examples/maven-app/github/*.yml /path/to/your/project/.github/workflows/
   # GitLab
   cp examples/maven-app/gitlab/.gitlab-ci.yml /path/to/your/project/
   # Forgejo, into a draft directory until every placeholder is filled
   cp examples/go-cli/forgejo/*.yml /path/to/drafts/
   ```

3. **Customize configuration:**
   - Update artifact `name`
   - Review artifact config versions and paths
   - Verify paths (working-directory, container-file)
   - Replace every placeholder and `FIXME` the file's header lists
   - Extend `nanolinter.toml` as you like; the gate's `required-lints` floor
     (default `secrets,sast`) runs whatever the plan says. Findings are reported
     by default; administrator lint enforcement makes them block. See
     [check enforcement](../docs/admin-policy.md) for administrator-owned policy.

4. **Configure release secrets:**
   Follow the [secrets and permissions reference](../docs/reference.md) and
   your forge's guide ([GitHub](../docs/github.md#secrets-and-tokens),
   [GitLab](../docs/gitlab.md#secrets-and-tokens),
   [Forgejo](../docs/forgejo.md#secrets-and-tokens)) for release
   authentication, the signing methods, and target-specific credentials such
   as Maven Central or Google Play.

5. **Create release:** `forge-ci setup release-request v1.0.0` checks your
   signing setup and prints these commands once it passes:
   ```bash
   git tag -s release-request/v1.0.0 -m "Release v1.0.0"
   git push origin release-request/v1.0.0
   ```

   The release validates this request and creates the final `v1.0.0` tag after
   release preparation. Do not create or push the final tag directly.

---

## At a glance

The GitHub configuration of each example:

| Example     | Project type   | Container path     | Publishing target               |
|-------------|----------------|--------------------|---------------------------------|
| Maven App   | Maven          | single, GHCR       | GHCR                            |
| NPM App     | NPM            | single, GHCR       | GitHub Packages                 |
| Gradle JVM  | Gradle         | optional           | none; GitHub Packages or Maven Central with `publish-to` |
| Android App | Gradle Android | optional           | Google Play                     |
| Cargo App   | Cargo          | one or many        | container only                  |
| Go CLI      | Go             | none               | GitHub Release binaries         |
| Go Service  | Go             | single, multi-arch | container only                  |
| Monorepo    | Maven + NPM    | one per artefact   | GHCR + GitHub Packages          |

---

## Common Modifications

- [Maven Central publishing](../docs/publishing.md#maven-central): library
  selection, Sonatype setup and the `MAVEN_CENTRAL_*` secrets.
- [npm publishing](../docs/publishing.md#npm-packages-github-packages): scoped
  packages for GitHub Packages.
- [Container configuration](../docs/artifacts-reference.md#containers-section):
  optional containers, platforms, security gates and SBOMs.
- [Build secrets](../docs/artifacts-reference.md#build-secrets): pass
  credentials to a container build as secrets, not as build arguments.

## Testing Your Configuration

[Testing consumer configuration](../docs/runtime-images.md#testing-consumer-configuration)
shows how to validate the YAML, run a branch snapshot and try a disposable
signed release request, also against an unreleased forge-ci candidate.

## Troubleshooting

See the [configuration troubleshooting reference](../docs/artifacts-reference.md#configuration-troubleshooting).

## See Also

- [Artifacts Reference](../docs/artifacts-reference.md) - Complete field documentation
- [Publishing Guide](../docs/publishing.md) - Registry setup instructions
