# Android App Example

Example configuration for Android applications with product flavors, APK and
AAB builds, release signing and optional Google Play upload.

## Per forge

- **GitHub**: three workflows, with `artifacts.yml`:
  - [`github/build-workflow.yml`](github/build-workflow.yml): a development
    build on push to `main`, or when run by hand.
  - [`github/release-workflow.yml`](github/release-workflow.yml): the release,
    started by a signed `release-request/vX.Y.Z` tag. After validation and
    version preparation, it creates the final `vX.Y.Z` tag and the GitHub
    Release, and can publish to Google Play. Pushing a final tag directly
    does not start it.
  - [`github/release-snapshot-workflow.yml`](github/release-snapshot-workflow.yml):
    run by hand, it builds a signed release AAB and uploads it to the Google
    Play internal track. It needs `GOOGLE_PLAY_SERVICE_ACCOUNT_JSON`, a
    service account JSON key with Google Play publishing permissions.

  See the [GitHub guide](../../docs/github.md#getting-started).
- **GitLab**: [`gitlab/.gitlab-ci.yml`](gitlab/.gitlab-ci.yml), the lint gate,
  `check` and `build-gradle-android`. GitLab has no Google Play component and
  no snapshot flow. See the [GitLab guide](../../docs/gitlab.md#getting-started).
- **Forgejo**: not available; the Forgejo layer has build actions for Go, Cargo and Maven only. See
  [Delivery layers side by side](../../docs/providers.md#delivery-layers-side-by-side).

The rest of this page describes the GitHub configuration.

## Runtime image

The Android workflow runs in the forge-ci Android runtime image, which
contains the supported JDK, Gradle and Android SDK. Pin the digest of the
release you call; the snippets below carry a `FIXME` placeholder.

## Calling the build

A signed build of the `prod` flavor:

```yaml
jobs:
  build:
    uses: diggsweden/forge-ci/.github/workflows/build-gradle-android.yml@PIN_FORGE_CI_SHA # v0.90.0
    secrets:  # Required for signing
      ANDROID_KEYSTORE: ${{ secrets.ANDROID_KEYSTORE }}
      ANDROID_KEYSTORE_PASSWORD: ${{ secrets.ANDROID_KEYSTORE_PASSWORD }}
      ANDROID_KEY_ALIAS: ${{ secrets.ANDROID_KEY_ALIAS }}
      ANDROID_KEY_PASSWORD: ${{ secrets.ANDROID_KEY_PASSWORD }}
    with:
      forge-ci-binary-ref: PIN_FORGE_CI_SHA # v0.90.0
      runtime-image: ghcr.io/diggsweden/forge-ci-runtime-android-35@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest
      build-module: "app"
      product-flavor: "prod"
      enable-signing: true
```

The secrets are the base64-encoded keystore (`ANDROID_KEYSTORE`), its
password, the key alias and the key password. Leave out `secrets:` and
`enable-signing` for an unsigned build.

Other inputs you are likely to change:

- `build-types`: `debug,release` by default; `"release"` skips the debug build.
- `include-aab`: also builds an AAB for Google Play; on by default.
- `product-flavor`: one flavor per call; for two flavors, add a second job
  that calls the workflow with the other flavor.
- `artifact-name-prefix` and `include-date-stamp`: the upload names, for
  example `<date> - testing_store - my-app - demo - APK release`.

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

## Library variant

A module that applies `com.android.library` is built in library mode: its
unit tests and its release AAR, `<module>/build/outputs/aar/*.aar`, and a
Build SBOM. A library has no keystore signing, no APK or AAB and no Google
Play, and its version is the `version` property of `gradle.properties`, not
`versionName` and `versionCode`. Declare it with `build-type: library`:

```yaml
artifacts:
  - name: my-android-library
    project-type: gradle-android
    build-type: library
    sboms: build
    publish-to: [forge-packages]
    config:
      build-module: lib
      gradle-version-file: gradle.properties
```

A direct call of the build sets `library: true`, on GitHub and on the GitLab
`build-gradle-android` component alike:

```yaml
jobs:
  build:
    uses: diggsweden/forge-ci/.github/workflows/build-gradle-android.yml@PIN_FORGE_CI_SHA # v0.90.0
    with:
      forge-ci-binary-ref: PIN_FORGE_CI_SHA # v0.90.0
      runtime-image: ghcr.io/diggsweden/forge-ci-runtime-android-35@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest
      library: true
      build-module: "lib"
```

The checks of a pull or merge request name the module's own tasks, for
example `./gradlew --no-daemon lib:test lib:lintRelease lib:assembleRelease`.
Publishing the AAR as a Maven publication needs `maven-publish` in the
module; see [Android libraries](../../docs/publishing.md#android-libraries).
Library mode needs v0.91.0 or later.

## Input Parameters

All inputs, defaults and output names:
[Android component reference](../../docs/components.md#build-gradle-androidyml).

## See Also

- [Android Gradle Plugin Documentation](https://developer.android.com/build)
- [Product Flavors Guide](https://developer.android.com/build/build-variants)
- [App Signing Documentation](https://developer.android.com/studio/publish/app-signing)
- [Google Play Developer API](https://developers.google.com/android-publisher)
