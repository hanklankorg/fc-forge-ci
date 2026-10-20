# Hello iOS

An XcodeGen iOS application with unit-test sources, a neutral bundle identity
and `versions.xcconfig` for Forge CI's marketing-version bump. No certificate,
profile or production publisher is included.

The GitHub PR workflow runs Forge CI's lint gate and a separate macOS ARM64
simulator build/test job. It asserts the runner architecture, selects Xcode
26.6, tests on the iPhone 17 / iOS 26.5 simulator and retains the xcresult bundle.
These selections match the macos-26 ARM64 runner inventory checked 2026-10-19.
The macOS ARM64 run is independent of the deferred Linux multi-architecture work.

A branch workflow dispatch additionally probes Forge CI's unsigned archive
path. The signing-capable build leaf refuses PR context; the fixture preserves
that guard rather than bypassing it. Neither path proves signed IPA export.

On a Mac, generate with `xcodegen generate`, then run `xcodebuild test -project
HelloTrial.xcodeproj -scheme HelloTrial -destination '<available simulator>'`.
Select a real available simulator rather than treating archive success as a
test result. Signed release and dev publishing require a separate authorized
trial certificate/profile and explicit acceptance; they are not wired yet.
Forgejo/GitLab execution remains blocked on suitable macOS runners.
