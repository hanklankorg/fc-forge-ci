# Hello Android library

A minimal Java Android library: one `com.android.library` module, `lib`, with
unit tests and its version read from the `version` property of
`gradle.properties`. It is the library beside the Android app demo,
`hello-android`, with the same Gradle wrapper, Android Gradle Plugin and
SDK 35, the line of the Forge CI Android runtime image.

With Android SDK 35 installed, run `./gradlew lib:test lib:lintRelease
lib:assembleRelease`. The release AAR is `lib/build/outputs/aar/lib-release.aar`.
A library has no keystore, no APK or AAB and no Google Play: `.forge-ci/artifacts.yml`
declares it `build-type: library`, and Forge CI then builds `lib:test` and
`lib:assembleRelease`.

GitHub pull requests run the Forge CI lint gate and a separate build and test
job. A branch dispatch also calls Forge CI's Android builder with
`library: true`, which uploads the AAR and a build SBOM. On GitLab the
merge-request gate lints in a nanolinter image and runs the same build and
tests in the Android runtime image, as its nonroot user. That image is
linux/amd64 only.

The two forges name different nanolinter flavours. The GitHub workflow pins
`kotlin`, the flavour Forge CI maps to `project-type: gradle-android` and the
one a consumer following its documentation pins. The GitLab lab lane has no
Kotlin lint image and supplies the Java flavour: the source is Java and the
plan in `nanolinter.toml` is `secrets` and `sast`, which need no Kotlin
toolchain. No live test lints Kotlin source.

The module declares one Maven publication, `release`, of the release variant
with its sources: `org.example.trial:hello-android-library` at the version of
`gradle.properties`. It names no repository and signs nothing. Forge CI
exports it to a directory (`forge-ci build gradle export-publications
--project lib`) and publishes the exported files, as
`publish-to: [forge-packages]` in `.forge-ci/artifacts.yml` asks.

On GitHub a signed `release-request/vX.Y.Z` tag starts the release
(`test/live/github/workflows/release.yml`): the orchestrator bumps the
version, builds the AAR in the Android runtime image, publishes the exported
publication to GitHub Packages in the Java runtime image and signs the
release with GPG, which writes to no transparency log. The build uploads the
AAR as the workflow artifact `hello-android-library` and the publication as
`hello-android-library-maven-publications`.

No release is wired on GitLab or Forgejo. There is no Forgejo workflow: the
Forgejo lane has no Android SDK job image.
