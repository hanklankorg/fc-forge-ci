# Administrator check enforcement

Forge CI separates **running a check**, **reporting its findings**, and
**blocking progression**. The administrator's engine distribution owns three
settings, all enabled by default:

```json
{
  "enforce-lint": true,
  "enforce-vulnerabilities": true,
  "enforce-application-tests": true
}
```

Setting a value to `false` permits that class of recognized findings to be
reported without failing the gate. It does not skip the check and does not turn
findings into a clean pass. Compilation, packaging, scanner startup, invalid
reports, cancellation, credentials, signatures, source binding and digest
verification are outside these exceptions.

## Administration and immutable authority

Administrators change `internal/domain/enforcement/policy.json` in their
independently controlled Forge CI distribution, review the change, build the
engine and its runtime images, and update their protected pipeline pins and
binary/image digests together. A tools-only signer image need not be rebuilt
when its tool contract is unchanged; the signing job must still fetch and verify
the newly pinned engine. The file is embedded at build time. Omitted
settings default to `true`; unknown keys, duplicate keys and non-boolean values
are refused. A malformed embedded policy fails gates closed.

Inspect the installed engine with:

```bash
forge-ci policy show
```

The output includes the settings and SHA-256 of the embedded policy bytes.
Finding decisions also print that digest, the check class, actual findings and
whether enforcement is enabled.

There is no consumer `--enforce-*` flag, environment override, policy-file path,
artifact override or mutation command. A file named `policy.json` in a consumer
checkout has no effect. The legacy `skip-tests=true` input is refused, including
`SKIP_TESTS=true`; report-only tests must still run. Maven test-skipping/ignore
options and Gradle task exclusions are refused at the engine's build gates.
`enable-scan=false` cannot disable the scan of an exported/published layout.
Forgejo lint/build actions checksum-verify their pinned engine even when a
consumer supplies `FORGE_CI_BIN`. GitLab's Nanolinter component bootstraps and
verifies that engine before lint execution, independently of optional SAST upload.

The protected pipeline must select the administrator's immutable engine and
workflow. GitLab uses the independently pinned external CI policy; GitHub and
Forgejo administrators must protect their wrappers, bootstrap coordinates and
credential-bearing workflow environments. A consumer-controlled engine build is
not administrator policy. Keep the same reviewed distribution in producer and
trusted signing jobs: the signer independently scans the delivered image bytes.
Producer variables and reports never become signing-policy authority.

## Lint and dependency findings

The shipped Nanolinter callers run once with full SARIF on stdout, then apply
the two policies independently:

```bash
forge-ci policy nanolinter --sarif-file nanolinter-security.sarif -- \
  nanolinter verify --require secrets,sast --report sarif --out -
```

OSV, Trivy filesystem, Go vulnerability and Rust audit findings use
`enforce-vulnerabilities`. Known lint/source checks use `enforce-lint`.
Unclassified security or external findings conservatively require both policies
to allow continuation. Disabling lint therefore cannot waive an independently
enforced vulnerability gate. Nanolinter configuration/runtime failures and a
failed invocation without classified findings remain blocking.
Structured operational-failure categories and the supported older report's
`tool failed` diagnostics remain blocking even if Nanolinter itself exits `1`.
The optional SARIF destination contains security results for existing provider upload steps;
its previous regular-file report is retired before execution, so a failed run
cannot upload that stale report as current evidence.

The check-selection floor still belongs in the administrator's trusted caller.
Enforcement is not proof that a consumer's test code is honest, that its chosen
plan covers every dependency, or that an arbitrary editable workflow executed
the approved gates. Provider protection and source review remain the authority
for those contracts. Unintegrated custom lint engines keep their own blocking
behavior; these settings are not a generic `continue-on-error` switch.

## Application tests

The Go, Maven, npm, Cargo and Gradle/Android build paths apply the application
test policy after running tests. A non-enforcing engine only continues after an
ordinary native test-failure status with a recognized fresh summary. Unknown,
missing, malformed or oversized output stays blocking. Maven reports containing
test setup errors are not assertion-only waivers; for example a missing
Testcontainers socket remains an error. Compilation and packaging invocations
are never waived. Where packaging needs a follow-up after failed tests, that
follow-up does not repeat the already reported tests.

Custom trusted callers can use the same gate, including an iOS simulator job:

```bash
forge-ci policy tests --protocol go-json -- go test -json ./...
forge-ci policy tests --protocol xctest -- \
  xcodebuild test -scheme Example -destination 'platform=iOS Simulator,name=iPhone 17'
```

Protocols are `go-json`, `maven`, `cargo`, `node` (Node test-runner/TAP summary
with a native assertion diagnostic),
`gradle` and `xctest`. Other npm test frameworks remain blocking when their
failure output cannot be classified; disabling enforcement does not authorize
guessing that an unknown tool error is an assertion failure. An archive-only
Xcode build does not itself claim simulator-test coverage. Select the protocol
and command literally in the trusted caller, not from consumer-produced data.

Existing test summaries say whether tests executed or were absent/skipped; read
the policy decision and original findings alongside that execution status. A
successful non-enforcing job is not evidence of a findings-free application.
