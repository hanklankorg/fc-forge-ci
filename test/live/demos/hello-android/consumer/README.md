# Hello Android

A minimal Java Android app with a demo flavor, unit tests and versionName /
versionCode read from `gradle.properties`. The standard Gradle wrapper verifies
its distribution checksum. SDK 35 deliberately matches the current Forge CI
Android runtime; this does not prove SDK 37 compatibility for the real consumer.

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

For the separate signed acceptance case, pass a disposable keystore through
Forge CI's signing inputs. The build reads its temporary path and passwords
from `ANDROID_KEYSTORE_PATH`, `ANDROID_KEYSTORE_PASSWORD`, `ANDROID_KEY_ALIAS`
and `ANDROID_KEY_PASSWORD`. No key is committed. Verify resulting APK/AAB
identity, versions and signatures, and prove private artifact access separately.

Signed releases, development publishing and Forgejo/GitLab workflows remain to
be wired and tested. Google Play is not configured for this disposable fixture.
