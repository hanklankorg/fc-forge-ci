# Android App Example

Example configuration for Android applications with multiple product flavors and build types.

## Per forge

- **GitHub**: [`github/build-workflow.yml`](github/build-workflow.yml),
  [`github/release-workflow.yml`](github/release-workflow.yml) and
  [`github/release-snapshot-workflow.yml`](github/release-snapshot-workflow.yml),
  with `artifacts.yml`. See the
  [GitHub guide](../../docs/github.md#getting-started).
- **GitLab**: [`gitlab/.gitlab-ci.yml`](gitlab/.gitlab-ci.yml), the lint gate,
  `check` and `build-gradle-android`. GitLab has no Google Play component and
  no snapshot flow. See the [GitLab guide](../../docs/gitlab.md#getting-started).
- **Forgejo**: not available; the Forgejo layer builds Go and Cargo only. See
  [Delivery layers side by side](../../docs/providers.md#delivery-layers-side-by-side).

The rest of this page describes the GitHub configuration.

> **Unpublished 0.90.0:** `@v0.90.0` references below are prospective. Until the tag
> and matching runtime images exist, use the reviewed-branch procedure in
> [Runtime Images](../../docs/runtime-images.md); do not substitute an
> unreviewed moving ref in production.

## Use Case

Android applications that need to:
- Build multiple variants (debug, release)
- Generate APK and AAB files
- Use custom artifact naming
- Support product flavors (demo, prod, staging, etc.)
- Sign release builds

## Files

### `github/build-workflow.yml`
Development build workflow that creates Android artifacts on push to main.

### `artifacts.yml`
Artifact configuration for release builds (used with `github/release-workflow.yml`).

### `github/release-workflow.yml`
Production release workflow triggered by signed `release-request/vX.Y.Z` tags.
After validation and version preparation, automation creates the final
`vX.Y.Z` tag, creates the GitHub Release, and optionally publishes to Google
Play. Direct final-tag pushes do not trigger this example workflow.

### `github/release-snapshot-workflow.yml`
Manual snapshot/testing release workflow that builds and uploads to Google Play internal track. This mirrors the iOS `release-snapshot-workflow.yml` which uploads to TestFlight.

## Features

### Multiple Build Types
- **Debug APK**: Development builds with debugging enabled
- **Release APK**: Optimized production builds
- **AAB (Android App Bundle)**: For Google Play Store distribution

### Custom Artifact Naming
Artifacts are named with date stamps and custom prefixes:
```text
2025-01-15 - testing_store - my-app - demo - APK debug
2025-01-15 - testing_store - my-app - demo - APK release
2025-01-15 - testing_store - my-app - demo - AAB release
```

### Product Flavors
Support for multiple flavors:
- `demo` - Demo/testing version
- `prod` - Production version
- `staging` - Staging environment
- Custom flavors as needed

### Runtime Image
The Android workflow runs in the forge-ci Android runtime image, which
contains the supported JDK, Gradle, and Android SDK toolchain. Normal consumers
use the workflow default; override `runtime-image` only for branch testing,
mirroring, or stricter reproducibility.

## Configuration

### Basic Setup

```yaml
jobs:
  build:
    uses: diggsweden/forge-ci/.github/workflows/build-gradle-android.yml@v0.90.0
    with:
      build-module: "app"
      product-flavor: "demo"
```

### With Signing

```yaml
jobs:
  build:
    uses: diggsweden/forge-ci/.github/workflows/build-gradle-android.yml@v0.90.0
    secrets:  # Required for signing
      ANDROID_KEYSTORE: ${{ secrets.ANDROID_KEYSTORE }}
      ANDROID_KEYSTORE_PASSWORD: ${{ secrets.ANDROID_KEYSTORE_PASSWORD }}
      ANDROID_KEY_ALIAS: ${{ secrets.ANDROID_KEY_ALIAS }}
      ANDROID_KEY_PASSWORD: ${{ secrets.ANDROID_KEY_PASSWORD }}
    with:
      build-module: "app"
      product-flavor: "prod"
      enable-signing: true
```

**Required Secrets:**
- `ANDROID_KEYSTORE` - Base64-encoded keystore file
- `ANDROID_KEYSTORE_PASSWORD` - Keystore password
- `ANDROID_KEY_ALIAS` - Key alias
- `ANDROID_KEY_PASSWORD` - Key password

The workflow maps `ANDROID_KEYSTORE` to the internal `ANDROID_KEYSTORE_BASE64`
environment variable used by the `forge-ci` CLI. Create the repository secret
as `ANDROID_KEYSTORE`.

### Release Only (No Debug)

```yaml
jobs:
  build:
    uses: diggsweden/forge-ci/.github/workflows/build-gradle-android.yml@v0.90.0
    secrets:  # Required for signing
      ANDROID_KEYSTORE: ${{ secrets.ANDROID_KEYSTORE }}
      ANDROID_KEYSTORE_PASSWORD: ${{ secrets.ANDROID_KEYSTORE_PASSWORD }}
      ANDROID_KEY_ALIAS: ${{ secrets.ANDROID_KEY_ALIAS }}
      ANDROID_KEY_PASSWORD: ${{ secrets.ANDROID_KEY_PASSWORD }}
    with:
      build-module: "app"
      build-types: "release"  # Only release builds
      include-aab: true
      enable-signing: true
```

### Custom Naming

```yaml
jobs:
  build:
    uses: diggsweden/forge-ci/.github/workflows/build-gradle-android.yml@v0.90.0
    with:
      build-module: "app"
      artifact-name-prefix: "production_store"  # Custom prefix
      include-date-stamp: true  # Include date in name
```

## Multi-Flavor Builds

Build multiple flavors in parallel:

```yaml
jobs:
  build-demo:
    uses: diggsweden/forge-ci/.github/workflows/build-gradle-android.yml@v0.90.0
    with:
      build-module: "app"
      product-flavor: "demo"
      artifact-name-prefix: "demo_store"

  build-prod:
    uses: diggsweden/forge-ci/.github/workflows/build-gradle-android.yml@v0.90.0
    secrets:  # Required for signing
      ANDROID_KEYSTORE: ${{ secrets.ANDROID_KEYSTORE }}
      ANDROID_KEYSTORE_PASSWORD: ${{ secrets.ANDROID_KEYSTORE_PASSWORD }}
      ANDROID_KEY_ALIAS: ${{ secrets.ANDROID_KEY_ALIAS }}
      ANDROID_KEY_PASSWORD: ${{ secrets.ANDROID_KEY_PASSWORD }}
    with:
      build-module: "app"
      product-flavor: "prod"
      artifact-name-prefix: "production_store"
      enable-signing: true
```

## Gradle Configuration

Your Android project needs standard Gradle configuration:

### `gradle.properties`
```properties
versionName=1.0.0
versionCode=1
```

### `build.gradle.kts` (app module)
```kotlin
android {
    defaultConfig {
        versionCode = project.property("versionCode").toString().toInt()
        versionName = project.property("versionName").toString()
    }

    flavorDimensions += "version"
    productFlavors {
        create("demo") {
            dimension = "version"
            applicationIdSuffix = ".demo"
        }
        create("prod") {
            dimension = "version"
        }
    }

    signingConfigs {
        create("release") {
            storeFile = file(System.getenv("ANDROID_KEYSTORE_PATH") ?: "release.keystore")
            storePassword = System.getenv("ANDROID_KEYSTORE_PASSWORD")
            keyAlias = System.getenv("ANDROID_KEY_ALIAS")
            keyPassword = System.getenv("ANDROID_KEY_PASSWORD")
        }
    }

    buildTypes {
        release {
            signingConfig = signingConfigs.getByName("release")
        }
    }
}
```

## Input Parameters

The [Android component reference](../../docs/components.md#build-gradle-androidyml)
owns the input/default table, output paths and derived-versus-overridden artifact
naming rules. This example uses the `app` module and the `demo`/`prod` flavors
shown above; the publishing job consumes the build's `aab-name` output below.

## Workflow Comparison: iOS vs Android

| Workflow | iOS | Android |
|----------|-----|---------|
| **Release (Production)** | Release-request-triggered, version bump, changelog, TestFlight, GitHub Release | Release-request-triggered, version bump, changelog, Google Play, GitHub Release |
| **Release Snapshot (Testing)** | Manual trigger, build + TestFlight only | Manual trigger, build + Google Play internal track |

### Release Snapshot Workflow

The `release-snapshot-workflow.yml` is for testing builds before a formal release:

```yaml
# Manual trigger - no version bump, no changelog, no GitHub release
# Just build and upload to Google Play internal track
name: Release Snapshot Workflow
on:
  workflow_dispatch:

jobs:
  build:
    uses: diggsweden/forge-ci/.github/workflows/build-gradle-android.yml@v0.90.0
    secrets:  # Required for signing
      ANDROID_KEYSTORE: ${{ secrets.ANDROID_KEYSTORE }}
      ANDROID_KEYSTORE_PASSWORD: ${{ secrets.ANDROID_KEYSTORE_PASSWORD }}
      ANDROID_KEY_ALIAS: ${{ secrets.ANDROID_KEY_ALIAS }}
      ANDROID_KEY_PASSWORD: ${{ secrets.ANDROID_KEY_PASSWORD }}
    with:
      build-module: app
      product-flavor: demo
      build-types: release
      include-aab: true
      enable-signing: true

  upload-play-store:
    needs: build
    uses: diggsweden/forge-ci/.github/workflows/publish-google-play.yml@v0.90.0
    secrets:
      GOOGLE_PLAY_SERVICE_ACCOUNT_JSON: ${{ secrets.GOOGLE_PLAY_SERVICE_ACCOUNT_JSON }}
    with:
      branch: ${{ github.ref }}
      aab-artifact-name: ${{ needs.build.outputs.aab-name }}
      package-name: com.example.myapp
      track: internal  # Equivalent to TestFlight
```

**Required Secrets for Google Play:**
- `GOOGLE_PLAY_SERVICE_ACCOUNT_JSON` - Service account JSON key with Google Play publishing permissions

## See Also

- [Android Gradle Plugin Documentation](https://developer.android.com/build)
- [Product Flavors Guide](https://developer.android.com/build/build-variants)
- [App Signing Documentation](https://developer.android.com/studio/publish/app-signing)
- [Google Play Developer API](https://developers.google.com/android-publisher)
