# Hello Android

A minimal Java Android app with a demo flavor, unit tests and versionName /
versionCode read from `gradle.properties`. The standard Gradle wrapper verifies
its distribution checksum. SDK 35 matches the Forge CI Android runtime image.

With Android SDK 35 installed, run `./gradlew testDemoDebugUnitTest
assembleDemoDebug assembleDemoRelease bundleDemoRelease`. GitHub PRs run the
Forge CI lint gate and a separate secret-free build/test job. A branch dispatch
also calls Forge CI's signing-capable Android builder and requests tests, APKs,
AAB and a build SBOM. The build is initially unsigned; debug keystore signing is
not release-signing evidence.

Forge CI's default release task selection must also run the selected module's
enabled unit tests. A successful APK/AAB build alone is not test evidence: inspect fresh
`app/build/test-results/` after a clean build. An explicit task override owns
its test selection; `skip-tests` deliberately omits the default unit-test tasks.

To sign, pass a throwaway keystore through Forge CI's signing inputs. The
build reads its path and passwords from `ANDROID_KEYSTORE_PATH`,
`ANDROID_KEYSTORE_PASSWORD`, `ANDROID_KEY_ALIAS` and `ANDROID_KEY_PASSWORD`.
No key is committed.

On GitLab the merge-request gate lints in a nanolinter image and runs the
same unsigned build and tests in the Android runtime image, as its nonroot
user. That image is linux/amd64 only.

The two forges name different nanolinter flavours. The GitHub workflow pins
`kotlin`, the flavour Forge CI maps to `project-type: gradle-android` and the
one a consumer following its documentation pins. The GitLab lab lane has no
Kotlin lint image and supplies the Java flavour: the source is Java and the
plan in `nanolinter.toml` is `secrets` and `sast`, which need no Kotlin
toolchain. No live test lints Kotlin source.

The demo has no release, no development publishing and no Forgejo workflow;
the Forgejo lane has no Android SDK job image. It does not publish to Google
Play.
