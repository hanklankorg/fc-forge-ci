<!-- SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government -->
<!-- SPDX-License-Identifier: CC0-1.0 -->

# Live acceptance runs

Read [`docs/live-runs.md`](docs/live-runs.md) before you schedule live runs
on Forge Lab or hosted GitHub.

- Plan the lanes first and check the host's free memory, CPU and disk.
  Independent Forgejo, GitLab and GitHub lanes may run side by side.
- Never unlink a lock file, and never split HOME/XDG or copy a contract to
  get past a lock.
- Keep each lane's source frozen and its evidence separate.
- Read a lane's outcome from the `verdict.json` beside its log. A partial
  lane pass is not provider or repository coverage.
- When moving script logic, planning, validation, policy and result
  interpretation go to Go; see
  [`docs/workflow-design-policy.md`](docs/workflow-design-policy.md).
