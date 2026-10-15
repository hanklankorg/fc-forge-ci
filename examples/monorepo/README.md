# Monorepo Example

Multiple artifacts built from a single repository with container support.

## Per forge

- **GitHub**: [`github/release-workflow.yml`](github/release-workflow.yml),
  with `artifacts.yml` or `multi-artifact-container.yml`. See the
  [GitHub guide](../../docs/github.md#getting-started).
- **GitLab**: [`gitlab/.gitlab-ci.yml`](gitlab/.gitlab-ci.yml), the lint gate,
  `check`, and on tags the plan-driven fan-out: a child pipeline with one
  `build-<ecosystem>` component per artifact of `artifacts.yml`. See the
  [GitLab guide](../../docs/gitlab.md#builds).
- **Forgejo**: not available; the Forgejo layer builds Go and Cargo only and
  has no plan-driven fan-out. See [Delivery layers side by side](../../docs/providers.md#delivery-layers-side-by-side).

The rest of this page describes the GitHub configuration.

## Project Structure

```text
my-monorepo/
├── services/
│   ├── backend/
│   │   ├── src/
│   │   ├── pom.xml
│   │   └── Containerfile
│   └── worker/
│       ├── src/
│       └── build.gradle.kts
├── apps/
│   └── frontend/
│       ├── src/
│       ├── package.json
│       └── Containerfile
├── .forge-ci/
│   └── artifacts.yml
└── .github/
    └── workflows/
        └── release-workflow.yml
```

## Configuration Examples

This directory contains multiple monorepo configuration examples:

### 1. Basic Monorepo - [artifacts.yml](artifacts.yml)

**Use case:** Build multiple artifacts, each with their own container

**Contains:**
- Maven backend (Java 25 runtime image)
- NPM frontend (Node 24 runtime image)

**Result:** 2 containers + frontend package

---

### 2. Multi-Artifact Container - [multi-artifact-container.yml](multi-artifact-container.yml)

**Use case:** Combine multiple builds into ONE container

**Contains:**
- API service (Maven)
- Worker service (Gradle JVM)
- Web frontend (NPM)

**Result:** 1 combined container with all three artifacts

---

## How Monorepo Builds Work

```text
1. Parse artifacts.yml
   └─> Identify all artifacts and containers

2. Build Stage (parallel)
    ├─> Build backend (Maven)
   └─> Build frontend (NPM)

3. Publish Stage (parallel)
    ├─> backend container → GHCR
    ├─> frontend package → GitHub Packages
   └─> frontend container → GHCR

4. Release Stage
   └─> Single GitHub release with all artifacts
```

## Key Features

### Unified Versioning
All artifacts share the version requested by the signed release-request tag.
Automation creates the final version tag after every bump succeeds:
```bash
git tag -s release-request/v1.0.0 -m "Release v1.0.0"
# All artifacts become version 1.0.0
```

### Independent Publishing
Each artifact can publish to different targets:
```yaml
artifacts:
  - name: backend
    project-type: maven
    # The application JAR is a release asset. Applications are not published
    # as Maven packages; declare a container separately when wanted.

  - name: frontend
    project-type: npm
    publish-to: [forge-packages]

  # If you need Maven shared libraries and Maven applications in the same repo,
  # use one Maven reactor/root artifact today so fixed Maven upload names do not collide.
```

### Flexible Container Dependencies
Containers reference artifacts by name:
```yaml
containers:
  # Single artifact container
  - name: backend
    from: [backend]
    context: .

  # Multi-artifact container
  - name: combined
    from: [api, worker, web]
    context: .
```

## Getting Started

### 1. Choose Your Example

**For separate containers:**
```bash
mkdir -p .forge-ci
cp examples/monorepo/artifacts.yml .forge-ci/artifacts.yml
```

**For combined container:**
```bash
mkdir -p .forge-ci
cp examples/monorepo/multi-artifact-container.yml .forge-ci/artifacts.yml
```

### 2. Customize Configuration

Update artifact names and paths to match your structure:
```yaml
artifacts:
  - name: backend
    project-type: maven
    working-directory: services/backend  # Match your structure
```

### 3. Create Workflow

```bash
cp examples/monorepo/github/release-workflow.yml .github/workflows/
```

### 4. Release

Configure release secrets first, including `RELEASE_TOKEN`, GPG signing secrets,
and any target-specific credentials such as Maven Central. See
[Reference Guide](../../docs/reference.md).

```bash
git tag -s release-request/v1.0.0 -m "Release v1.0.0"
git push origin release-request/v1.0.0
```

Do not push `v1.0.0` directly; the orchestrator creates it once at the prepared
branch HEAD after all artifact version bumps succeed.

## Common Patterns

Use the [monorepo reference](../../docs/artifacts-reference.md#monorepo-configuration)
for the general separate-container and combined-container patterns. The two
files in this directory are concrete instances: the basic example separates
the Maven backend and NPM frontend, while the multi-artifact variant combines
Maven, Gradle and NPM outputs.

## Limitations

The [shared monorepo limitations](../../docs/artifacts-reference.md#monorepo-limitations)
apply to both examples, including unified versioning, fixed Maven upload names
and serialized release preparation. Use one Maven reactor/root artifact for
dependent Maven modules rather than separate colliding uploads.

## Troubleshooting

For missing artifact names, build dependencies and unpacked Containerfile paths,
use [Assembly and dependency ordering](../../docs/artifacts-reference.md#assembly-and-dependency-ordering).
Those rules are shared with other consumers rather than repeated per example.

## See Also

- [Artifacts Reference](../../docs/artifacts-reference.md)
- [Publishing Guide](../../docs/publishing.md)
