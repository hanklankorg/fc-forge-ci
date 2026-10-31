<!-- SPDX-FileCopyrightText: The Forge CI Authors -->
<!-- SPDX-License-Identifier: CC0-1.0 -->

# Live acceptance campaigns

For work using Forge Lab or hosted GitHub, read
[`docs/live-campaigns.md`](docs/live-campaigns.md) before scheduling live runs.

- Inventory cases, provider/road, dependencies, mutable resource coordinates and
  estimated peak CPU/RAM/disk. Check available host headroom and runner capacity.
- Default to asynchronous independent provider lanes when capacity permits.
  Choose another ready, compatible case while a lane waits on a dependency.
  Record the actual reason whenever otherwise independent lanes are serialized.
- Use `FORGE_CI_LIVE_PROVIDER=forgejo` or `gitlab` for concurrent focused local
  suites. Keep one guarded hosted GitHub organization session; prepare its shared
  SDK once and reuse it for independent consumer cases.
- Coordinate target/runner changes, local image preparation and shared services.
  A shared Compose network/DinD alias is a cross-provider resource, not isolation.
  Keep each lane's source frozen and its evidence separate.
- Preserve lock files, acknowledgements, source/digest binding and exact cleanup
  authority. Use the supported scope instead of changing HOME/XDG or copying
  target contracts to evade a lock.
- Keep the requested OS/architecture exclusions explicit in the campaign
  inventory. A partial lane pass is not complete provider or repository coverage.
- Inspect script responsibilities before converting them. Planning, validation,
  policy and result interpretation belong in Go/CLI owners; shell can retain
  build/exec glue. Preserve tested interruption and cleanup behavior during moves.
