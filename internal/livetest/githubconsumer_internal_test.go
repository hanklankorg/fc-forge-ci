// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/google/go-github/v91/github"
	"golang.org/x/crypto/nacl/box"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

var trialEngine = GitHubEngine{Owner: "HankLankOrg", Repository: "fc-forge-ci", Commit: strings.Repeat("a", 40)} //nolint:gochecknoglobals // read-only fixture.

func TestFailedJobAnnotationsUseExactRepositoryAndBoundOutput(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/repos/fixture/fc-app/check-runs/42/annotations" || r.URL.Query().Get("per_page") != "10" {
			t.Errorf("unexpected check annotation request: %s", r.URL)
		}
		_ = json.NewEncoder(w).Encode([]map[string]string{{"message": "Runner unavailable: " + strings.Repeat("x", 3000)}})
	}))
	defer server.Close()
	api, err := github.NewClient(github.WithHTTPClient(server.Client()), github.WithURLs(github.Ptr(server.URL+"/"), nil))
	if err != nil {
		t.Fatal(err)
	}
	tb := &recordingTB{}
	trial := GitHubTrial{tb: tb, api: api, org: "fixture"}
	trial.reportJobAnnotations(t.Context(), "fc-app", &github.WorkflowJob{CheckRunURL: github.Ptr("https://api.github.com/repos/fixture/fc-app/check-runs/42")})
	if calls != 1 || len(tb.logs) != 1 || !strings.Contains(tb.logs[0], "Runner unavailable:") || !strings.HasSuffix(tb.logs[0], "[truncated]") || len(tb.logs[0]) > 2200 {
		t.Fatalf("missing bounded annotation: calls=%d logs=%v", calls, tb.logs)
	}
	trial.reportJobAnnotations(t.Context(), "fc-app", &github.WorkflowJob{CheckRunURL: github.Ptr("https://other.invalid/repos/fixture/fc-app/check-runs/42")})
	trial.reportJobAnnotations(t.Context(), "fc-app", &github.WorkflowJob{CheckRunURL: github.Ptr("https://api.github.com/repos/fixture/another/check-runs/42")})
	if calls != 1 {
		t.Fatal("unexpected check URL changed the scoped API request")
	}
}

func TestFailureLogKeepsCauseAfterLargeProgressLine(t *testing.T) {
	t.Parallel()
	log := "download " + strings.Repeat("progress ", 20000) + "\nAffected target: usr/bin/pebble\n##[error]three blocking vulnerabilities\npost-job cleanup\n"
	got := readFailureExcerpt(strings.NewReader(log), 3)
	for _, want := range []string{"line shortened", "Affected target: usr/bin/pebble", "##[error]three blocking vulnerabilities"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if len(got) > 4096 || strings.Contains(got, "post-job cleanup") {
		t.Fatalf("failure excerpt is noisy or unbounded: %d bytes", len(got))
	}
}

func TestFailureLogReportsIncompleteReadWithoutPrivateError(t *testing.T) {
	t.Parallel()
	got := readFailureExcerpt(iotest.ErrReader(errors.New("private signed URL")), 40)
	if !strings.Contains(got, "job log incomplete") || strings.Contains(got, "private signed URL") {
		t.Fatalf("unexpected incomplete-log diagnostic: %s", got)
	}
}

const fixturePullRequest = `jobs:
  pr-checks:
    uses: diggsweden/forge-ci/.github/workflows/pullrequest-orchestrator.yml@v0.90.0
    with:
      forge-ci-binary-ref: v0.90.0
      runtime-image: ghcr.io/diggsweden/forge-ci-runtime-base:v0.90.0
      runtime-image-go: "ghcr.io/diggsweden/forge-ci-runtime-go-1.27:v0.90.0"
      nanolinter-image: ghcr.io/itiquette/nanolinter:go
  test:
    steps:
      - uses: actions/checkout@df4cb1c069e1874edd31b4311f1884172cec0e10 # v6.0.3
`

// The fixture's pins are the published engine's; rendered, every one names
// the engine under test: its workflows at the commit, the rolling pre-release
// binary, and the runtime images built for the commit, in lowercase GHCR.
func TestRenderGitHubConsumer_PinsTheEngineUnderTest(t *testing.T) {
	t.Parallel()

	got, _, err := RenderGitHubConsumer("pullrequest.yml", fixturePullRequest, trialEngine, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"uses: HankLankOrg/fc-forge-ci/.github/workflows/pullrequest-orchestrator.yml@" + trialEngine.Commit,
		"forge-ci-binary-ref: v0.90.0-pre",
		"runtime-image: ghcr.io/hanklankorg/forge-ci-runtime-base:sha-aaaaaaa",
		`"ghcr.io/hanklankorg/forge-ci-runtime-go-1.27:sha-aaaaaaa"`,
		"nanolinter-image: ghcr.io/itiquette/nanolinter:go",
		"uses: actions/checkout@df4cb1c069e1874edd31b4311f1884172cec0e10",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered fixture lacks %q:\n%s", want, got)
		}
	}

	if strings.Contains(got, "diggsweden") {
		t.Errorf("rendered fixture still names diggsweden:\n%s", got)
	}
}

// A setting replaces a value the workflow passes, keeping its indentation, and
// reports which inputs it replaced, so the trial can refuse a setting that no
// workflow of the fixture passes rather than silently ignore it.
func TestRenderGitHubConsumer_Overrides(t *testing.T) {
	t.Parallel()

	mirror := "ghcr.io/hanklankorg/nanolinter:go@sha256:" + strings.Repeat("d", 64)

	const input = "nanolinter-image"

	got, applied, err := RenderGitHubConsumer("pullrequest.yml", fixturePullRequest, trialEngine, map[string]string{input: mirror})
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(applied, []string{input}) {
		t.Errorf("applied = %q, want the nanolinter image", applied)
	}

	if !strings.Contains(got, "\n      "+input+": "+mirror+"\n") {
		t.Errorf("override not applied in place:\n%s", got)
	}

	unchanged, applied, err := RenderGitHubConsumer("pullrequest.yml", fixturePullRequest, trialEngine, map[string]string{"megalinter-image": "x"})
	if err != nil || len(applied) != 0 || strings.Contains(unchanged, "megalinter-image") {
		t.Errorf("override of an absent input: applied %q, err %v; want nothing applied and no error", applied, err)
	}
}

// A workflow that calls an engine the trial does not replace would run the
// published layer, so rendering refuses it; so does an incomplete engine.
func TestRenderGitHubConsumer_RefusesWhatItCannotPin(t *testing.T) {
	t.Parallel()

	foreign := "jobs:\n  x:\n    uses: someone/forge-ci/.github/workflows/release-orchestrator.yml@main\n"
	if _, _, err := RenderGitHubConsumer("release.yml", foreign, trialEngine, nil); !errors.Is(err, errs.ErrValidation) {
		t.Errorf("foreign engine: err = %v, want a validation error", err)
	}

	if _, _, err := RenderGitHubConsumer("release.yml", fixturePullRequest, GitHubEngine{Owner: "o", Repository: "r", Commit: "abc"}, nil); !errors.Is(err, errs.ErrValidation) {
		t.Errorf("short commit: err = %v, want a validation error", err)
	}
}

// The keyless identity matches the engine's own reusable workflows and no
// other repository's.
func TestKeylessIdentityRegexp(t *testing.T) {
	t.Parallel()

	identity := regexp.MustCompile(trialEngine.KeylessIdentityRegexp())

	for subject, want := range map[string]bool{
		"https://github.com/HankLankOrg/fc-forge-ci/.github/workflows/release-create-github.yml@refs/heads/main":   true,
		"https://github.com/HankLankOrg/fc-forge-ci/.github/workflows/publish-container.yml@" + trialEngine.Commit: true,
		"https://github.com/HankLankOrg/fc-forge-ciX/.github/workflows/release.yml@refs/heads/main":                false,
		"https://github.com/other/fc-forge-ci/.github/workflows/release.yml@refs/heads/main":                       false,
	} {
		if got := identity.MatchString(subject); got != want {
			t.Errorf("identity matches %s = %v, want %v", subject, got, want)
		}
	}
}

// What is sealed opens with the repository's private key and nothing else.
func TestSealGitHubSecret_OpensWithTheRepositoryKey(t *testing.T) {
	t.Parallel()

	public, private, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	sealed, err := SealGitHubSecret(base64.StdEncoding.EncodeToString(public[:]), "s3cret value")
	if err != nil {
		t.Fatal(err)
	}

	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		t.Fatal(err)
	}

	opened, ok := box.OpenAnonymous(nil, raw, public, private)
	if !ok || string(opened) != "s3cret value" {
		t.Fatalf("sealed secret does not open to the value: ok=%v %q", ok, opened)
	}

	if _, err := SealGitHubSecret("not-a-key", "x"); !errors.Is(err, errs.ErrValidation) {
		t.Errorf("bad key: err = %v, want a validation error", err)
	}
}

func TestReleaseRequestTag(t *testing.T) {
	t.Parallel()

	request, final, err := ReleaseRequestTag("v0.1.0")
	if err != nil || request != "release-request/v0.1.0" || final != "v0.1.0" {
		t.Fatalf("ReleaseRequestTag(v0.1.0) = %q, %q, %v", request, final, err)
	}

	for _, bad := range []string{"0.1.0", "v0.1", "v01.0.0", "v0.1.0-rc1"} {
		if _, _, err := ReleaseRequestTag(bad); !errors.Is(err, errs.ErrValidation) {
			t.Errorf("ReleaseRequestTag(%q) err = %v, want a validation error", bad, err)
		}
	}
}

func TestParseGitHubTrialSettings(t *testing.T) {
	t.Parallel()

	pinned := "ghcr.io/o/nanolinter:go@sha256:" + strings.Repeat("d", 64)

	release, err := ParseGitHubTrialSettings(TrialRelease, []string{"version=v1.2.3", "image=gommitlint", "nanolinter-image=" + pinned, "fixtures=tests/live/github"})
	if err != nil {
		t.Fatal(err)
	}

	if release.Workflow != "release.yml" || release.Version != "v1.2.3" || release.Image != "gommitlint" ||
		release.Fixtures != "tests/live/github" || release.Overrides["nanolinter-image"] != pinned {
		t.Errorf("release settings = %+v", release)
	}

	// version= is X.Y.Z on every forge; a leading v is accepted.
	plain, err := ParseGitHubTrialSettings(TrialRelease, []string{"version=1.2.3"})
	if err != nil || plain.Version != "v1.2.3" {
		t.Errorf("version=1.2.3: %+v, %v; want v1.2.3", plain, err)
	}

	pr, err := ParseGitHubTrialSettings(TrialPullRequest, nil)
	if err != nil || pr.Workflow != "pullrequest.yml" || pr.Version != "" || pr.Platforms != "linux/amd64" {
		t.Errorf("pull-request defaults = %+v, %v", pr, err)
	}

	for stated, engine := range map[string]string{
		"linux/amd64": "linux/amd64", "linux/arm64": "linux/amd64,linux/arm64", "linux/amd64,linux/arm64": "linux/amd64,linux/arm64",
	} {
		if got := GitHubEnginePlatforms(stated); got != engine {
			t.Errorf("GitHubEnginePlatforms(%q) = %q, want %q", stated, got, engine)
		}
	}

	// amd64 unless the run states more; each runs on its own native runner.
	both, err := ParseGitHubTrialSettings(TrialPullRequest, []string{"RELEASE_PLATFORMS=linux/amd64,linux/arm64"})
	if err != nil || both.Platforms != "linux/amd64,linux/arm64" {
		t.Errorf("RELEASE_PLATFORMS=both: %+v, %v", both, err)
	}

	for _, bad := range [][2]string{
		{TrialPullRequest, "version=v1.0.0"},
		{TrialPullRequest, "image=x"},
		{TrialPullRequest, "RELEASE_PLATFORMS=linux/riscv64"},
		{TrialPullRequest, "RELEASE_PLATFORMS=linux/amd64,linux/amd64"},
		{TrialPullRequest, "RELEASE_PLATFORMS=linux/amd64, linux/arm64"},
		{TrialPullRequest, "platforms=linux/amd64"},
		{TrialRelease, "version=1.0"},
		{TrialRelease, "fixtures=../elsewhere"},
		{TrialRelease, "runtime-image=x"},
		{TrialRelease, "nanolinter-image=a b"},
		{TrialRelease, "nanolinter-image=ghcr.io/o/nanolinter:go"},
		{TrialRelease, "nanolinter-image=ghcr.io/o/nanolinter:go@sha256:d3888d6b"},
		{"push", ""},
	} {
		words := []string{bad[1]}
		if bad[1] == "" {
			words = nil
		}

		if _, err := ParseGitHubTrialSettings(bad[0], words); !errors.Is(err, errs.ErrValidation) {
			t.Errorf("ParseGitHubTrialSettings(%s, %q) err = %v, want a validation error", bad[0], bad[1], err)
		}
	}
}

func TestParseGitHubTrialSettings_MultipleImages(t *testing.T) {
	t.Parallel()
	settings, err := ParseGitHubTrialSettings(TrialRelease, []string{"image=worker,keytool,bff"})
	if err != nil || settings.Image != "worker,keytool,bff" {
		t.Fatalf("multi-image selection = %+v, %v", settings, err)
	}
	for _, value := range []string{"worker,worker", "worker,", ",worker", "worker, keytool", "worker/../other"} {
		if _, err := ParseGitHubTrialSettings(TrialRelease, []string{"image=" + value}); !errors.Is(err, errs.ErrValidation) {
			t.Errorf("invalid image selection %q accepted: %v", value, err)
		}
	}
}

func TestParseGitHubTrialSettings_RejectsRepeatedImageSelectors(t *testing.T) {
	t.Parallel()
	for _, words := range [][]string{
		{"image=worker", "image=bff"},
		{"image=worker", "image=worker"},
		{"image=worker,bff", "image=load-tests"},
	} {
		if _, err := ParseGitHubTrialSettings(TrialRelease, words); !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "supplied once") {
			t.Errorf("repeated selectors %q silently dropped a verification target: %v", words, err)
		}
	}
}

func TestParseGitHubTrialSettings_DevelopmentRelease(t *testing.T) {
	t.Parallel()
	settings, err := ParseGitHubTrialSettings(TrialDevRelease, nil)
	if err != nil || settings.Workflow != "release-dev.yml" || settings.Version != "" || settings.Image != "" {
		t.Fatalf("development settings = %+v, %v", settings, err)
	}
	for _, word := range []string{"version=1.2.3", "image=app"} {
		if _, err := ParseGitHubTrialSettings(TrialDevRelease, []string{word}); !errors.Is(err, errs.ErrValidation) {
			t.Errorf("stable-only setting %q accepted for development: %v", word, err)
		}
	}
}

// A failed job's log ends with its cleanup steps; the excerpt is the lines
// leading to the first error annotation, and the end only without one.
func TestFailureExcerpt(t *testing.T) {
	t.Parallel()

	log := []string{"setup", "step", "boom", "##[error]Process completed with exit code 1.", "post cleanup", "agent noise"}

	if got := FailureExcerpt(log, 2); !slices.Equal(got, []string{"boom", "##[error]Process completed with exit code 1."}) {
		t.Errorf("with an error: %q", got)
	}

	if got := FailureExcerpt(log[4:], 5); !slices.Equal(got, []string{"post cleanup", "agent noise"}) {
		t.Errorf("without an error: %q", got)
	}
}

// A keyless release verifies against the engine's workflow identity; a
// key-signed one against the trial's key, with no log entry required, since
// it keeps its signatures out of the public log.
func TestCosignTrust_VerifyArgs(t *testing.T) {
	t.Parallel()

	engine := GitHubEngine{Owner: "org", Repository: "fc-forge-ci", Commit: strings.Repeat("a", 40)}

	keyless := KeylessTrust(engine).verifyArgs()
	if !slices.Equal(keyless, []string{"--certificate-identity-regexp", engine.KeylessIdentityRegexp(), "--certificate-oidc-issuer", githubIssuer}) {
		t.Errorf("keyless args = %q", keyless)
	}

	key := TrialCosignKey{path: "/trial/cosign.pub"}.Trust()
	if args := key.verifyArgs(); !slices.Equal(args, []string{"--key", "/trial/cosign.pub", "--insecure-ignore-tlog=true"}) {
		t.Errorf("key args = %q", args)
	}

	if key.String() != "the trial's Cosign key" || KeylessTrust(engine).String() != engine.KeylessIdentityRegexp() {
		t.Errorf("trust names: %q, %q", key, KeylessTrust(engine))
	}
}

// An orchestrator call that leaves its images to their released defaults
// would fail in its first job on an unreleased engine; it is refused before
// anything is pushed, and the same call with runtime-image renders.
func TestRenderGitHubConsumer_RequiresTheRuntimeImageOfAnOrchestratorCall(t *testing.T) {
	t.Parallel()

	engine := GitHubEngine{Owner: "org", Repository: "fc-forge-ci", Commit: strings.Repeat("a", 40)}
	call := "jobs:\n  release:\n    uses: diggsweden/forge-ci/.github/workflows/release-orchestrator.yml@v0.90.0\n    with:\n      branch: main\n"

	if _, _, err := RenderGitHubConsumer("release.yml", call, engine, nil); !errors.Is(err, errs.ErrValidation) ||
		!strings.Contains(err.Error(), "runtime-image") {
		t.Fatalf("RenderGitHubConsumer() = %v, want the missing runtime-image named", err)
	}

	withImage := call + "      runtime-image: ghcr.io/diggsweden/forge-ci-runtime-base:v0.90.0\n"

	rendered, _, err := RenderGitHubConsumer("release.yml", withImage, engine, nil)
	if err != nil || !strings.Contains(rendered, "runtime-image: "+engine.Registry()+"/forge-ci-runtime-base:"+engine.ImageTag()) {
		t.Fatalf("RenderGitHubConsumer() = %v:\n%s", err, rendered)
	}
}
