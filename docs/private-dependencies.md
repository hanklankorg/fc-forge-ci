# Private build dependencies

Private dependency access is explicit and separate from package publication.
Enable `config.private-dependencies: true` on an npm, Maven, or Gradle JVM
artifact. Configuration and CLI preflight report missing settings or credentials
before running a package manager.

## GitHub workflows

The standard build workflows request `packages: read` and expose the run token
only to the selected build step. The calling workflow must grant `packages: read`
too. Fork pull requests are refused before checkout when private dependencies
are enabled. A package in another repository must grant this consumer repository
access in GitHub's package settings; there is no automatic PAT fallback.

## npm

```yaml
config:
  private-dependencies: true
  dependency-registry: https://npm.pkg.github.com
```

The project's `.npmrc` chooses which scopes use that registry. Forge CI writes
only a temporary, host/path-scoped authentication line; it does not change the
default registry. The selected credential reaches `npm ci` and its installation
hooks. The temporary configuration is removed before tests, build, SBOM generation,
and packing; those subprocesses do not inherit the dependency credential.

## Maven

```yaml
config:
  private-dependencies: true
  settings-path: .forge-ci/maven/settings.xml
```

The settings path is relative to the Maven job's working directory. Keep the
project's mirrors, proxies, and repository declarations there. Forge CI never
overwrites or merges this file. For GitHub Packages, a server whose ID matches
the POM repository can use:

```xml
<server>
  <id>github</id>
  <username>${env.DEPENDENCY_USERNAME}</username>
  <password>${env.DEPENDENCY_TOKEN}</password>
</server>
```

Maven can resolve dependencies and plugins throughout its lifecycle, so the
credential is available to its configured Maven invocations, including metadata
evaluation. The settings selection applies to all of them.

## Gradle JVM

Enable `config.private-dependencies: true`. The project's repository declarations
read `DEPENDENCY_USERNAME` and `DEPENDENCY_TOKEN`, for example with Gradle's
`providers.environmentVariable(...)`. Forge CI does not inject repositories or
rewrite build scripts. Credential-bearing invocations use `--no-daemon`.

## GitLab, Forgejo, and direct CLI use

Provide a read-scoped `DEPENDENCY_TOKEN` (or `--dependency-token-file`) and, when
the repository protocol needs it, `DEPENDENCY_USERNAME`. The GitLab components
expose the same private-dependency inputs. Forgejo consumers can call the same
CLI commands in their own trusted build jobs. No token is automatically promoted
from package-publishing credentials. GitLab job-token cross-project access and
Forgejo token scopes remain provider/operator configuration.

Use the CLI's `--private-dependencies` flag explicitly. npm also requires
`--dependency-registry`; Maven requires `--settings-path`. Missing credentials,
insecure non-loopback registry URLs, and missing settings are early failures.

## Credential boundary

Project installation hooks and Maven/Gradle plugins are executable code and can
read credentials passed to their process. This feature is for trusted builds
with narrowly scoped read credentials, not a sandbox for untrusted project code.
Forge CI redacts the selected token from diagnostic streams and errors, never
passes it on argv, and never includes it in configuration plans or build artifacts.
Do not put credentials into repository URLs or committed settings.
