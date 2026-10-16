# Configuration Examples

Complete working examples for different project types.

> Each example follows one of the two ecosystem patterns (A: build artefact + container COPY; B: container is the build environment). See **[docs/ecosystems.md](../docs/ecosystems.md)** for the full framing and per-ecosystem capability matrices.
>
> Examples target the v0.90.0 workflow contract. Before that release tag and its
> matching runtime images exist, use the branch-testing flow in
> **[docs/runtime-images.md](../docs/runtime-images.md)**.
>
> Image digests such as `PIN_DIGEST` are placeholders. Nanolinter's GHCR migration
> destination is `ghcr.io/itiquette/nanolinter`; verify the chosen flavour is
> published and readable before replacing its digest, or use an available mirror.
> See **[lint images](../docs/components.md#lint-nanolinteryml-default)**.

## Available Examples

One directory per ecosystem, shared by all three forges. Each holds the
forge-neutral `artifacts.yml`, a README with a short "Per forge" list, and a
subdirectory per forge that builds it: `github/` with the caller workflows,
`gitlab/.gitlab-ci.yml`, and `forgejo/pullrequest.yml` with
`forgejo/release.yml`. A forge without a directory cannot build that
ecosystem; [Delivery layers side by side](../docs/providers.md#delivery-layers-side-by-side)
has the capability table.

| Ecosystem | GitHub | GitLab | Forgejo |
|---|---|---|---|
| [Go CLI](go-cli/): standalone binaries, cross-compiled per platform | [workflows](go-cli/github/) | [pipeline](go-cli/gitlab/.gitlab-ci.yml) | [workflows](go-cli/forgejo/) |
| [Go Service](go-service/): a multi-arch container, compiled in the Containerfile (Pattern B) | [workflows](go-service/github/) | [pipeline](go-service/gitlab/.gitlab-ci.yml) | [workflows](go-service/forgejo/) |
| [Rust Application](cargo-app/): a Cargo workspace shipped as containers, with the private-registry build-secrets pattern | [workflows](cargo-app/github/) | [pipeline](cargo-app/gitlab/.gitlab-ci.yml) | [workflows](cargo-app/forgejo/) |
| [Maven Application](maven-app/): a Java service shipped as a multi-platform container (Pattern A) | [workflows](maven-app/github/) | [pipeline](maven-app/gitlab/.gitlab-ci.yml) | not available |
| [Gradle JVM](gradle-app/): a JVM-only Gradle build | [workflows](gradle-app/github/) | [pipeline](gradle-app/gitlab/.gitlab-ci.yml) | not available |
| [NPM Application](npm-app/): a Node service published as a package and shipped as a container (Pattern A) | [workflows](npm-app/github/) | [pipeline](npm-app/gitlab/.gitlab-ci.yml) | not available |
| [Android Application](android-app/): APK and AAB with product flavors, optional Google Play upload | [workflows](android-app/github/) | [pipeline](android-app/gitlab/.gitlab-ci.yml) | not available |
| [Monorepo](monorepo/): several artifacts and project types, one container per artifact or one combined | [workflows](monorepo/github/) | [pipeline](monorepo/gitlab/.gitlab-ci.yml) | not available |

What each forge's files do differs with its layer. The GitHub workflows call
the orchestrators and read `artifacts.yml`. The GitLab pipelines are the
merge-request gate and build, from the CI/CD components; a release runs from
a separate policy project, as the [GitLab guide](../docs/gitlab.md#releases-and-promotion)
describes. The Forgejo pair puts your build job between the layer's prepare
and signer workflows; it reads no `artifacts.yml`, because the workflow is the
plan. The [Forgejo signer-image workflow](forgejo/) is for the organization
that serves the layer, not for a consumer.

### By signing backend: [`signing/`](signing/)

How a GitHub release is signed, independent of what is being released.
Combine one with any ecosystem example above. Each holds `artifacts.yml`, a
README and `github/release-workflow.yml`. The GitLab and Forgejo layers sign
with a Cosign key pair of the organization; see their guides.

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

4. **Configure release secrets:**
   Follow the [secrets and permissions reference](../docs/reference.md) and
   your forge's guide ([GitHub](../docs/github.md#secrets-and-tokens),
   [GitLab](../docs/gitlab.md#secrets-and-tokens),
   [Forgejo](../docs/forgejo.md#secrets-and-tokens)) for release
   authentication, the signing methods, and target-specific credentials such
   as Maven Central or Google Play.

5. **Create release:**
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
| Gradle JVM  | Gradle         | optional           | not wired today                 |
| Android App | Gradle Android | optional           | Google Play                     |
| Cargo App   | Cargo          | one or many        | container only                  |
| Go CLI      | Go             | none               | GitHub Release binaries         |
| Go Service  | Go             | single, multi-arch | container only                  |
| Monorepo    | Maven + NPM    | one per artefact   | GHCR + GitHub Packages          |

---

## Common Modifications

- [Maven Central publishing](../docs/publishing.md#maven-central) owns library
  selection, Sonatype setup and the `MAVEN_CENTRAL_*` secrets.
- [NPM publishing](../docs/publishing.md#npm-packages-github-packages) owns scoped
  packages and registry requirements. Current production support is GitHub
  Packages, not `npmjs.org`.
- [Container configuration](../docs/artifacts-reference.md#containers-section)
  owns optional container builds, platform selection, security gates and SBOM
  selection. Disabling a gate removes its guarantee.
- [Build secrets](../docs/artifacts-reference.md#build-secrets) owns the secret
  envelope and mount contract; credentials do not belong in build arguments.

## Testing Your Configuration

Follow [Testing consumer configuration](../docs/runtime-images.md#testing-consumer-configuration)
for YAML validation, the branch-only snapshot workflow, and a disposable signed
release request. The same page owns the reviewed-commit workflow/binary/image
pins required before 0.90.0 is published.

## Troubleshooting

The [configuration troubleshooting reference](../docs/artifacts-reference.md#configuration-troubleshooting)
owns missing config/Containerfile paths and successful builds without the
expected publication. Keep this index focused on choosing and adapting an
example rather than maintaining another copy of those contracts.

## See Also

- [Artifacts Reference](../docs/artifacts-reference.md) - Complete field documentation
- [Publishing Guide](../docs/publishing.md) - Registry setup instructions
