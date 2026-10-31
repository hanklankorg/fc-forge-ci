# Resource-aware asynchronous live campaigns

## Plan before launch

Build a short lane table with provider/road, case, frozen candidate, prerequisites,
mutable repository/package coordinates, expected peak RAM/CPU/disk, and evidence
destination. Read Forge Lab's current `AGENTS.md` and documented recipes before
operating its providers or runners.

Inspect host available memory, CPU/load, free disk, active jobs and runner
concurrency/limits. Forge Lab's read-only doctor/status interfaces and host
metrics provide the observations; configured runner limits and previous measured
peaks provide the budgets. Leave explicit headroom for the forges, compiler work
and cleanup. Unknown memory cost is a reason for a conservative budget, not for
assuming every CPU can host another Maven build.

Choose ready cases that fit the remaining budget. Independent Forgejo, GitLab
and hosted GitHub work should normally overlap. Hosted jobs consume remote
runner capacity, while their local orchestration, compilation and image
preparation still consume local resources. Reassess after each completion or
resource failure and start another compatible case instead of waiting behind an
unrelated provider.

## Execution scopes and locks

Focused local suites support `FORGE_CI_LIVE_PROVIDER=forgejo` or `gitlab`.
Selection is checked by Go preflight, runtime provider selection and runner
architecture selection. A single-provider run cannot claim the full profile.
The retained report names its execution provider and preserves the complete
contract identity/provenance.

The launcher takes the existing `live.lock` shared and its provider lock
exclusive. Different providers can overlap; the same provider remains exclusive
because fixtures use fixed repository names. Unscoped/all-provider and older
launchers take `live.lock` exclusively, so mixed old/new runs still conflict
safely. Locks remain held through test cleanup and are not inherited by suites
or loggers. Never unlink a lock file or split HOME/XDG to create concurrency.

For example, from a frozen controller:

```bash
FORGE_CI_LIVE_PROVIDER=forgejo just test-live '^TestForgejoNPMLibrary_RegistryConsumer$' &
forgejo_pid=$!
FORGE_CI_LIVE_PROVIDER=gitlab just test-live '^TestGitLabRelease_ResourceGroupSpansTheChild$' &
gitlab_pid=$!
status=0
wait "$forgejo_pid" || status=1
wait "$gitlab_pid" || status=1
exit "$status"
```

Supply each case's normal inputs and a separate evidence destination. Collect
every lane's result individually. For GitHub, use the guarded Forge Lab hosted
session recipe and its own organization lock; shared SDK preparation precedes
parallel read-only reuse by independent consumers.

## Shared dependencies

Provider separation alone does not prove independence. Target rotation, runner
reconciliation, package coordinates, local image builds and backing services can
be shared. In particular, the Compose runner's shared network and a `docker`
service alias can collide across jobs. Serialize that resource or choose cases
that do not require it; keep other ready lanes moving.

Record intentional serialization and its dependency/capacity reason. Keep source
snapshots immutable during runs and preserve actual statuses, findings and
cleanup receipts. Result classification is implemented in Go; a package-level
PASS with only skipped scenarios is not a successful live lane.
