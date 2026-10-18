// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package deps wires the platform-selected provider and CI sink adapters.
//
// Some CLI subcommands still construct narrow tool adapters directly,
// but provider/output/summary/manifest selection lives here so the
// platform-specific wiring has one home.
//
// Build(ctx) reads the platform via internal/adapters/platform.Detect() and
// returns a typed *Deps with adapters wired in.
package deps

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/forgejo"
	"github.com/diggsweden/forge-ci/internal/adapters/ghaoutput"
	"github.com/diggsweden/forge-ci/internal/adapters/github"
	"github.com/diggsweden/forge-ci/internal/adapters/gitlab"
	"github.com/diggsweden/forge-ci/internal/adapters/gitlaboutput"
	"github.com/diggsweden/forge-ci/internal/adapters/jsonsink"
	"github.com/diggsweden/forge-ci/internal/adapters/local"
	"github.com/diggsweden/forge-ci/internal/adapters/manifest"
	"github.com/diggsweden/forge-ci/internal/adapters/platform"
	"github.com/diggsweden/forge-ci/internal/adapters/stepsummary"
	"github.com/diggsweden/forge-ci/internal/domain/ci"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/domain/repository"
	"github.com/diggsweden/forge-ci/internal/runcontext"
)

// Deps is what use cases receive. Provider is the always-available
// minimum (Name + ResolveContext); CLI commands that need richer
// platform capabilities (release creation, asset upload, SARIF upload,
// token validation) request them via the typed accessors below, which
// surface a friendly "feature X requires platform Y" error when the
// detected platform does not support the role.
type Deps struct {
	Platform       provider.ForgeAPI
	Provider       provider.Provider
	OutputSink     ci.OutputSink
	SummarySink    ci.SummarySink
	ManifestSink   ci.ManifestSink
	JobResultStore ci.JobResultStore
}

// requireRole is the single gate behind every Deps.RequireX accessor: it
// returns the active provider's R role view, or a typed "capability is
// not supported on platform Y" error when the provider does not implement
// R. Each accessor below just names its role type and human-readable
// capability and delegates here, so the assert-or-explain logic lives in
// exactly one place.
func requireRole[R any](d *Deps, capability string) (R, error) {
	role, ok := d.Provider.(R)
	if !ok {
		var zero R

		return zero, unsupportedRoleError(d.Platform, capability)
	}

	return role, nil
}

// RequireTokenValidator returns the TokenValidator role when the
// active provider implements it. GitHub, GitLab and Forgejo do; local does
// not — invoking a CLI command that needs this on local surfaces a
// typed error here rather than a runtime ErrUnsupported deep in the
// use case.
func (d *Deps) RequireTokenValidator() (provider.TokenValidator, error) {
	return requireRole[provider.TokenValidator](d, "token validation")
}

// RequireReleaseCreator returns the ReleaseCreator role when the
// active provider implements it. GitHub, GitLab and Forgejo do; local does not.
func (d *Deps) RequireReleaseCreator() (provider.ReleaseCreator, error) {
	return requireRole[provider.ReleaseCreator](d, "release creation")
}

// RequireReleasePublisher returns the ReleasePublisher role when the active
// provider implements in-place release publishing/reconciliation.
func (d *Deps) RequireReleasePublisher() (provider.ReleasePublisher, error) {
	return requireRole[provider.ReleasePublisher](d, "release publishing")
}

// RequireReleaseAssetUploader returns the ReleaseAssetUploader role
// when the active provider implements it. GitHub, Forgejo and GitLab all
// implement it; local does not.
func (d *Deps) RequireReleaseAssetUploader() (provider.ReleaseAssetUploader, error) {
	return requireRole[provider.ReleaseAssetUploader](d, "release asset upload")
}

// RequireReleaseAssetPruner returns the ReleaseAssetPruner role when the
// active provider implements it. GitHub, GitLab and Forgejo do; local does not.
func (d *Deps) RequireReleaseAssetPruner() (provider.ReleaseAssetPruner, error) {
	return requireRole[provider.ReleaseAssetPruner](d, "release asset retention")
}

// RequireRunArtifactUploader returns the RunArtifactUploader role when
// the active provider implements it.
func (d *Deps) RequireRunArtifactUploader() (provider.RunArtifactUploader, error) {
	return requireRole[provider.RunArtifactUploader](d, "run-artifact upload")
}

// RequireForgeMavenRegistryResolver returns the ForgeMavenRegistryResolver
// role when the active forge exposes a native Maven package registry (GitHub /
// GitLab / Forgejo); local and registry-less forges yield an unsupported error.
func (d *Deps) RequireForgeMavenRegistryResolver() (provider.ForgeMavenRegistryResolver, error) {
	return requireRole[provider.ForgeMavenRegistryResolver](d, "forge-packages Maven deploy")
}

// RequireForgeNPMRegistryResolver returns the ForgeNPMRegistryResolver role —
// the npm sibling of RequireForgeMavenRegistryResolver.
func (d *Deps) RequireForgeNPMRegistryResolver() (provider.ForgeNPMRegistryResolver, error) {
	return requireRole[provider.ForgeNPMRegistryResolver](d, "forge-packages npm publish")
}

// RegistryAuthForDetected returns the RegistryAuthResolver for the
// auto-detected forge and true when that forge implements it. Mirrors
// SigningIdentityForDetected: false for local, so `container login` keeps its
// explicit-credentials behaviour.
func RegistryAuthForDetected() (provider.RegistryAuthResolver, bool) {
	p, err := providerFor(platform.Detect())
	if err != nil {
		return nil, false
	}

	r, ok := p.(provider.RegistryAuthResolver)

	return r, ok
}

// RequireRunArtifactDownloader returns the RunArtifactDownloader role
// when the active provider implements it.
func (d *Deps) RequireRunArtifactDownloader() (provider.RunArtifactDownloader, error) {
	return requireRole[provider.RunArtifactDownloader](d, "run-artifact download")
}

// RequireSARIFUploader returns the SARIFUploader role when the active
// provider implements it. Today only github does — gitlab consumes
// the JSON SAST report directly from trivy/opengrep.
func (d *Deps) RequireSARIFUploader() (provider.SARIFUploader, error) {
	return requireRole[provider.SARIFUploader](d, "SARIF upload to Code Scanning")
}

// RequireTagDeleter returns the TagDeleter role when the active provider
// implements it. Forge-specific: deleting a container tag goes through
// each forge's package API (Forgejo and GitLab do; GitHub and local not yet),
// because a generic OCI delete is unsafe on the shared-digest promotion
// model. Callers (e.g. `container ledger cleanup`) gate here.
func (d *Deps) RequireTagDeleter() (provider.TagDeleter, error) {
	return requireRole[provider.TagDeleter](d, "container tag deletion")
}

// RepoMetadataFetcher returns the RepoMetadataFetcher role. All three
// adapters implement it (local returns empty), so this never errors —
// the helper exists purely so callers depend on the narrow role
// interface rather than the concrete provider.
func (d *Deps) RepoMetadataFetcher() provider.RepoMetadataFetcher {
	return d.Provider.(provider.RepoMetadataFetcher) //nolint:forcetypeassert // every wired Provider implements it; compile-time enforced by adapter conformance vars
}

// WebURLBuilder returns the role when the active provider implements it, and
// nil when it does not. Absence is an ordinary outcome here rather than an
// error: a platform with no hosted web UI renders a placeholder, so this does
// not go through requireRole.
func (d *Deps) WebURLBuilder() provider.WebURLBuilder {
	builder, _ := d.Provider.(provider.WebURLBuilder)

	return builder
}

func unsupportedRoleError(p provider.ForgeAPI, capability string) error {
	return fmt.Errorf("%s is not supported on platform %q: %w", capability, p, errs.ErrUnsupported)
}

// Build produces a *Deps wired for the detected platform. Most CLI actions
// should use With so sink close errors are propagated consistently.
//
// Errors are pre-wrapped with "init deps:" so every CLI Action can
// simply `return err` without restating the context — the wrap
// belongs to Build's contract (its job IS to init deps), not to each
// of the 40+ subcommands.
func Build(ctx context.Context) (*Deps, error) {
	d, err := buildInternal(ctx)
	if err != nil {
		return nil, fmt.Errorf("init deps: %w", err)
	}

	return d, nil
}

// WithFormat builds Deps, runs fn, and closes any sink resources.
// When format resolves to JSON the OutputSink is replaced by a
// jsonsink writing to the supplied stdout writer (defaults to
// os.Stdout when nil); other formats keep the platform-detected sink
// so workflows still write to $GITHUB_OUTPUT.
//
// Close errors are returned when fn succeeds so CI output flush
// failures cannot be hidden.
//
// CLI Actions use FromCmd, which reads the format from the root flag
// and forwards here. WithFormat is also the primitive for non-CLI
// callers (tests, library use) that already know the format.
func WithFormat(ctx context.Context, format output.Format, stdout io.Writer, fn func(*Deps) error) (err error) {
	d, err := Build(ctx) //nolint:varnamelen // idiomatic short name (testing/http/io conventions).
	if err != nil {
		return err
	}

	if format == output.FormatJSON {
		// Replace the platform sink with one that emits the structured
		// outputs as a JSON object on the chosen writer. The platform
		// sink is closed eagerly here so we never leave its handle
		// dangling — the json sink takes over the contract.
		if d.OutputSink != nil {
			_ = d.OutputSink.Close(ctx)
		}

		if stdout == nil {
			stdout = os.Stdout
		}

		d.OutputSink = jsonsink.New(stdout)
	}

	defer func() {
		if closeErr := d.Close(ctx); err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	return fn(d)
}

// FromCmd resolves the root format flag from cmd and forwards to
// WithFormat. The standard entry point for CLI Actions.
func FromCmd(ctx context.Context, cmd *cli.Command, fn func(*Deps) error) error {
	format, err := OutputFormat(cmd)
	if err != nil {
		return err
	}

	return WithFormat(ctx, format, os.Stdout, fn)
}

func buildInternal(_ context.Context) (*Deps, error) {
	// Two orthogonal axes: the forge API (which server to call) and the
	// runner conventions (which output dialect to emit). They resolve
	// independently — e.g. a self-hosted GitLab forge with a local runner —
	// and Forgejo is its own value on *both* axes (Gitea forge API + a
	// distinct runner dialect, not a GitHub-compatible one).
	forge := platform.Detect()
	runner := platform.DetectRunner()
	d := &Deps{Platform: forge} //nolint:varnamelen // idiomatic short name (testing/http/io conventions).

	if err := wireProvider(d, forge); err != nil {
		return nil, err
	}

	if err := wireSinks(d, runner); err != nil {
		return nil, err
	}

	// ManifestSink + JobResultStore: one manifest.Sink rooted at
	// $CI_RESULTS_DIR (default ".ci-results") serves both — stage manifests at
	// <stage>-result.json and per-job records under jobs/<job>.json.
	sink := manifest.NewFromEnv()
	d.ManifestSink = sink
	d.JobResultStore = sink

	return d, nil
}

// wireProvider selects the forge-API adapter for the detected platform.
func wireProvider(d *Deps, forge provider.ForgeAPI) error { //nolint:varnamelen // idiomatic short name (matches buildInternal's receiver-style d).
	p, err := providerFor(forge)
	if err != nil {
		return err
	}

	d.Provider = p

	return nil
}

// providerFor returns the forge-API adapter for a platform. This is the
// single provider-construction switch in the codebase; app/domain code
// asks the returned provider (via the Describer / Capabilities / role
// interfaces) rather than branching on the platform enum itself.
func providerFor(forge provider.ForgeAPI) (provider.Provider, error) {
	switch forge {
	case provider.ForgeGitHub:
		return github.New(), nil
	case provider.ForgeGitLab:
		return gitlab.New(), nil
	case provider.ForgeForgejo:
		return forgejo.New(), nil
	case provider.ForgeLocal:
		return local.New(), nil
	default:
		return nil, fmt.Errorf("unsupported platform: %q: %w", forge, errs.ErrValidation)
	}
}

// ProviderConfig is what a verb states about the forge on its own command
// line. An empty field leaves that value to the environment.
type ProviderConfig struct {
	// Token authenticates the forge API calls.
	Token string
	// ServerURL is the forge base URL.
	ServerURL string
	// Repository is "owner/repo".
	Repository string
}

// ParseRepository performs the detected forge's pure path preflight before
// token resolution or Build (which wires output sinks). Adapter construction
// only stores environment getters; ParseRepository never invokes them.
func ParseRepository(raw string) (repository.Path, error) {
	p, err := providerFor(platform.Detect())
	if err != nil {
		return repository.Path{}, err
	}

	parser, err := RoleFrom[repository.Parser](p, "repository addressing")
	if err != nil {
		return repository.Path{}, err
	}

	return parser.ParseRepository(raw)
}

// providerEnvKey names the variables one adapter reads one ProviderConfig value
// from.
type providerEnvKey struct {
	// name is the variable an explicit value is answered under.
	name string
	// chain is every variable the adapter can read the value from, where that
	// is more than name. Beside an explicit value the rest of the chain reads
	// as empty, so the explicit value wins wherever name stands in the
	// adapter's own order.
	chain []string
}

// providerEnvKeys names the variables one adapter reads the ProviderConfig
// values from. Platforms with no such variables (local) have none.
type providerEnvKeys struct {
	token, serverURL, repository providerEnvKey
	// ownTokens are the token variables that belong to this forge alone, in
	// the adapter's order. See ProviderTokenEnv.
	ownTokens []string
}

func providerEnvKeysFor(forge provider.ForgeAPI) providerEnvKeys {
	switch forge {
	case provider.ForgeGitHub:
		// The release client reads GH_TOKEN ahead of GITHUB_TOKEN, and the
		// package registries read the repository from the shared chain.
		// serverURL moves what the adapter describes, not where it calls: the
		// REST client takes its base from GITHUB_API_URL. No verb that passes
		// ProviderConfig.ServerURL asks GitHub for a role today, so nothing
		// reaches that gap; a GitHub role added behind ProviderWith must
		// overlay the API base too.
		tokens := []string{"GH_TOKEN", "GITHUB_TOKEN"}

		return providerEnvKeys{
			token:      providerEnvKey{name: "GITHUB_TOKEN", chain: tokens}, //nolint:gosec // G101: env var name, not a value.
			serverURL:  providerEnvKey{name: "GITHUB_SERVER_URL"},
			repository: providerEnvKey{name: "GITHUB_REPOSITORY", chain: runcontext.Repository().Keys()},
			ownTokens:  tokens,
		}
	case provider.ForgeGitLab:
		return providerEnvKeys{
			token:      providerEnvKey{name: "GITLAB_TOKEN"}, //nolint:gosec // G101: env var name, not a value.
			serverURL:  providerEnvKey{name: "CI_SERVER_URL"},
			repository: providerEnvKey{name: "CI_PROJECT_PATH"},
			ownTokens:  []string{"GITLAB_TOKEN"},
		}
	case provider.ForgeForgejo:
		// The adapter resolves all three through these runcontext chains, so
		// the names are taken from the chains rather than repeated here.
		return providerEnvKeys{
			token:      providerEnvKey{name: "FORGEJO_TOKEN", chain: runcontext.Token().Keys()}, //nolint:gosec // G101: env var name, not a value.
			serverURL:  providerEnvKey{name: "FORGEJO_SERVER_URL", chain: runcontext.ServerURL().Keys()},
			repository: providerEnvKey{name: "FORGEJO_REPOSITORY", chain: runcontext.Repository().Keys()},
			// Not the whole chain: CI_TOKEN is the cross-forge name, and
			// GITHUB_TOKEN is GitHub's own token off a Forgejo runner.
			ownTokens: []string{"FORGEJO_TOKEN", "GITEA_TOKEN"},
		}
	case provider.ForgeLocal:
		return providerEnvKeys{}
	}

	return providerEnvKeys{}
}

// ProviderTokenEnv names the variables that hold the detected forge's own API
// token, in the order its adapter reads them; none on a platform without one.
//
// It is for a verb that falls back from its own token source to the forge's:
// reading only these names keeps one forge's token from being handed, as an
// explicit ProviderConfig.Token, to another forge's server.
func ProviderTokenEnv() []string {
	return providerEnvKeysFor(platform.Detect()).ownTokens
}

// ProviderWith builds the adapter for the detected forge with cfg overlaid on
// the environment the adapter reads. The process environment is not changed:
// the values exist only in the returned adapter, so a credential passed here
// is not visible to a child process or to another adapter.
//
// It exists so a verb that takes --server-url, --repository or a token file
// stays forge-agnostic: the forge is detected from the environment rather than
// constructed for one adapter, and every adapter already resolves these values
// through its Env seam, so answering for the adapter's own variable names is
// the whole mechanism. A zero cfg is the ordinary env-detected provider.
//
// A non-empty cfg value is the one the adapter uses: the other variables it
// could read that value from are answered as empty, so a job variable earlier
// in the adapter's order (CI_TOKEN or CI_SERVER_URL on Forgejo) cannot replace
// it. They are emptied rather than given the value because the adapters also
// decide from the name which server a token may be sent to.
func ProviderWith(cfg ProviderConfig) (provider.Provider, error) {
	forge := platform.Detect()
	keys := providerEnvKeysFor(forge)
	overlay := make(map[string]string)

	for _, field := range []struct {
		key   providerEnvKey
		value string
	}{
		{keys.token, cfg.Token},
		{keys.serverURL, cfg.ServerURL},
		{keys.repository, cfg.Repository},
	} {
		if field.key.name == "" || field.value == "" {
			continue
		}

		for _, name := range field.key.chain {
			overlay[name] = ""
		}

		overlay[field.key.name] = field.value
	}

	if len(overlay) == 0 {
		return providerFor(forge)
	}

	return providerForWithEnv(forge, providerEnvironment(overlay))
}

// providerEnvironment overlays one adapter's inputs without changing the process
// environment. A present empty value deliberately masks an ambient fallback.
func providerEnvironment(overlay map[string]string) func(string) string {
	return func(name string) string {
		if value, ok := overlay[name]; ok {
			return value
		}

		return os.Getenv(name)
	}
}

// providerForWithEnv is providerFor with the adapter's environment source
// replaced. Every adapter carries the same Env seam, so this stays a single
// switch rather than per-forge construction at each call site.
func providerForWithEnv(forge provider.ForgeAPI, env func(string) string) (provider.Provider, error) {
	switch forge {
	case provider.ForgeGitHub:
		return &github.Provider{Env: env}, nil
	case provider.ForgeGitLab:
		return &gitlab.Provider{Env: env}, nil
	case provider.ForgeForgejo:
		return &forgejo.Provider{Env: env}, nil
	case provider.ForgeLocal:
		return &local.Provider{Env: env}, nil
	default:
		return nil, fmt.Errorf("unsupported platform: %q: %w", forge, errs.ErrValidation)
	}
}

// RoleFrom asserts a port role on a provider the caller built itself (e.g. via
// ProviderWith), producing the same typed "unsupported on this
// platform" refusal the Require* gates give. Keeping one spelling means a verb
// that resolves its own provider still degrades exactly like every other.
func RoleFrom[R any](p provider.Provider, capability string) (R, error) {
	role, ok := p.(R)
	if !ok {
		var zero R

		return zero, unsupportedRoleError(p.Name(), capability)
	}

	return role, nil
}

// DescriberForDetected returns the forge self-description for the
// auto-detected platform. CLI helpers that need provider metadata (e.g.
// the default OIDC issuer) outside a fully-built *Deps use this so they
// reuse the single providerFor factory instead of re-deriving the
// platform→provider mapping. Every adapter implements Describer, so the
// type assertion always succeeds; the local fallback is defensive.
func DescriberForDetected() provider.Describer {
	p, err := providerFor(platform.Detect())
	if err != nil {
		return local.New()
	}

	if d, ok := p.(provider.Describer); ok {
		return d
	}

	return local.New()
}

// SigningIdentityForDetected returns the SigningIdentityResolver for the
// execution runner and true when its forge implements the role. The local
// forge (and any without OIDC support) does not implement it, returning false
// so sign/verify callers keep their previous behaviour (operator supplies the
// keyless constraints explicitly) instead of getting a derived default.
func SigningIdentityForDetected() (provider.SigningIdentityResolver, bool) {
	var forge provider.ForgeAPI

	switch platform.DetectRunner() {
	case provider.RunnerGitHub:
		forge = provider.ForgeGitHub
	case provider.RunnerForgejo:
		forge = provider.ForgeForgejo
	case provider.RunnerGitLab:
		forge = provider.ForgeGitLab
	default:
		return nil, false
	}

	p, err := providerFor(forge)
	if err != nil {
		return nil, false
	}

	r, ok := p.(provider.SigningIdentityResolver)

	return r, ok
}

// CapabilitiesForDetected returns the optional-feature set of the
// auto-detected forge. Reporting surfaces (doctor) use it to show which
// behaviours degrade on the active forge. Every adapter implements
// CapabilityReporter; the empty set is the defensive fallback.
func CapabilitiesForDetected() provider.Capabilities {
	p, err := providerFor(platform.Detect())
	if err != nil {
		return provider.Capabilities{}
	}

	if c, ok := p.(provider.CapabilityReporter); ok {
		return c.Capabilities()
	}

	return provider.Capabilities{}
}

// wireSinks selects the output + step-summary sinks for the detected
// runner conventions.
//
// GitHub and Forgejo are separate cases, not one shared dialect. Both use
// a file-based step-output sink (Forgejo's act engine supports $*_OUTPUT),
// and Forgejo's native var is $FORGEJO_OUTPUT (with $GITHUB_OUTPUT as a
// compat alias since Forgejo Runner 7.0.0). Forgejo 17 with Runner 13.2
// renders job summaries from $FORGEJO_STEP_SUMMARY ($GITHUB_STEP_SUMMARY as
// its alias); an older Forgejo sets neither, so the summary goes to the job
// log there. GitLab renders no summary file at all: its sink keeps the file
// as a job artifact and also prints each summary as a job-log section.
func wireSinks(d *Deps, runner provider.RunnerKind) error { //nolint:varnamelen // idiomatic short name (matches buildInternal's receiver-style d).
	switch runner {
	case provider.RunnerGitHub:
		d.OutputSink = ghaoutput.NewFromEnv()
		d.SummarySink = stepsummary.New(os.Getenv("GITHUB_STEP_SUMMARY"))
	case provider.RunnerForgejo:
		d.OutputSink = ghaoutput.NewFromEnvVars("FORGEJO_OUTPUT", "GITHUB_OUTPUT")
		d.SummarySink = stepsummary.NewWithLog(cmp.Or(os.Getenv("FORGEJO_STEP_SUMMARY"), os.Getenv("GITHUB_STEP_SUMMARY")), os.Stdout)
	case provider.RunnerGitLab:
		d.OutputSink = gitlaboutput.NewFromEnv()
		d.SummarySink = stepsummary.NewGitLab(os.Getenv("CI_SUMMARY_FILE"), os.Stdout)
	case provider.RunnerLocal:
		d.OutputSink = ghaoutput.New(os.DevNull)
		d.SummarySink = stepsummary.New("")
	default:
		return fmt.Errorf("unsupported runner: %q: %w", runner, errs.ErrValidation)
	}

	return nil
}

// Close releases any resources held by the wired adapters. Today only
// OutputSink holds a file handle; new adapters that need cleanup register
// themselves here so Actions don't grow knowledge of which sinks happen to be
// closeable.
func (d *Deps) Close(ctx context.Context) error {
	if d == nil || d.OutputSink == nil {
		return nil
	}

	return d.OutputSink.Close(ctx)
}

// Annotator builds the CLI-side output.Annotator that writes to
// stderr in the format selected by the root --format flag (or --json
// shortcut). Hides the cmd.Root()/flag-name/platform-detect chain
// that would otherwise be replicated at every Action.
func Annotator(cmd *cli.Command) output.Annotator {
	return output.AnnotatorFromFlag(os.Stderr, rawFormat(cmd), platform.DetectRunner())
}

// OutputFormat returns the resolved output format selected by the root
// --format flag (or --json shortcut) — FormatAuto is resolved against
// the active CI platform. Hides the cmd.Root()/flag-name/platform-detect
// chain that would otherwise be replicated at every Action.
//
// Empty input (e.g. tests that build a subgroup directly and bypass
// the root flag default) is treated as FormatAuto, mirroring the
// root flag's Value default.
func OutputFormat(cmd *cli.Command) (output.Format, error) {
	raw := rawFormat(cmd)
	if raw == "" {
		raw = string(output.FormatAuto)
	}

	format, err := output.ParseAndResolve(raw, platform.DetectRunner())
	if err != nil {
		// A bad --format value is operator error: classify it as a usage
		// error (exit 2), matching root.go's Before-hook validation, rather
		// than letting it fall through to ExitCodeSoftware (70).
		return "", fmt.Errorf("%w: %w", err, errs.ErrUsage)
	}

	return format, nil
}

// rawFormat reads the root format selection, applying the --json
// shortcut. Kept in deps to avoid leaking the precedence rule into
// every Action; the equivalent logic in internal/cli/root.go's Before
// hook and this helper must move together.
func rawFormat(cmd *cli.Command) string {
	if cmd.Root().Bool("json") {
		return string(output.FormatJSON)
	}

	return cmd.Root().String("format")
}
