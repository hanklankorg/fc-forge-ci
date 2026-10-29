# Consumer PR unit fixtures

These are regular consumer PR proofs with deliberately bounded coverage:

| Previous filter | Current filter | Proof |
| --- | --- | --- |
| `TestForgejoMigrationShellPullRequest` | `TestForgejoConsumerPRUnitChecks` | Current-engine checkout plus original shell unit gates |
| `TestGitHubMigrationJavaPullRequest` | `TestGitHubConsumerPRUnitBaseline` | Original reusable workflow plus executed-job and nonzero Surefire evidence |
| `TestMigrationPRWorkflow` | `TestConsumerPRWorkflow` | Offline workflow rendering |
| `TestMigrationPRPublicInstallCredentialBoundary` | `TestConsumerPRPublicInstallCredentialBoundary` | Offline public-tool subprocess credential boundary |

The old filters have no aliases. Coordinate the new filters before launching a
replacement controller. Existing compiled controllers retain their old code.

Both live tests use `FORGE_CI_LIVE_CONSUMER` for the admitted clean source
checkout and `FORGE_CI_LIVE_CONSUMER_SETTINGS` for existing newline-delimited
trial settings, including `workflow=<filename>.yml` and literal variables.
Neither proof claims the complete engine PR/release flow. Engine-native
workflows should continue to use `TestForgejoLayer_ConsumerTrial` and its
`PlanConsumer` semantics.

The Forgejo overlay requires `FORGE_CI_LIVE_CONSUMER_PUBLIC_TOOLS=true`.
Its default shell fixture retains the complete unfiltered `mise install`,
the two original distro images/Just selections, and `just test-setup; just test`.
`FORGE_CI_LIVE_CONSUMER_PR_FIXTURE` optionally supplies a single typed JSON object
with `PublicTools`, `Distros` (one or two objects containing `Name`, `Image`,
`JustVersion`), `Prerequisites`, and `Checks`. All fields are required when
overriding. The engine action, digest, and contract runner are trial-owned.
Private tools require origin-scoped authentication; this public-only installer
must not be used to quietly proceed without it. Forge job credentials remain
available to repository operations but are removed from tool child processes.

The GitHub baseline requires the existing hosted SDK context and admitted
`FORGE_CI_LIVE_GITHUB_ORG_ID`, plus:

- `workflow=<original reusable workflow filename>` in consumer settings;
- `FORGE_CI_LIVE_CONSUMER_REPORT_KIND=surefire`;
- `FORGE_CI_LIVE_CONSUMER_EXPECTED_JOB=<original job display name>` (without
  the scratch caller prefix `original-unit-check / `);
- an external `FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR`.

The original reusable workflow must contain a single test job compatible with
appending report retention. The existing strict staging/export and Surefire
oracles are reused unchanged. Their internal artifact name remains
`migration-java-surefire`; it is an evidence protocol detail, not a feature.
Scratch repository creation stays with the existing source SDK guard.

Offline checks: `go test ./internal/livetest -run 'Test(ConsumerPR|Migration|GitHubStage)'`.
Compile live tests without running them: `go test -tags live ./internal/livetest -run '^$'`.
