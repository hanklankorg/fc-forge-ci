# Hello iOS

An XcodeGen iOS application with unit-test sources, a neutral bundle identity
and `versions.xcconfig` for Forge CI's marketing-version bump. No certificate,
profile or production publisher is included.

The GitHub PR workflow runs Forge CI's lint gate and a separate macOS ARM64
simulator build/test job. It asserts the runner architecture, selects Xcode
26.6, tests on the iPhone 17 / iOS 26.5 simulator and keeps the xcresult
bundle. These match what the GitHub `macos-26` ARM64 runner offers.

A branch workflow dispatch additionally probes Forge CI's unsigned archive
path. The signing-capable build leaf refuses PR context; the fixture preserves
that guard rather than bypassing it. Neither path proves signed IPA export.

On a Mac, generate with `xcodegen generate`, then run `xcodebuild test -project
HelloTrial.xcodeproj -scheme HelloTrial -destination '<available simulator>'`.
Select a real available simulator rather than treating archive success as a
test result. The demo has no signed release and no development publishing,
and runs only on GitHub: the Forgejo and GitLab labs have no macOS runner.
