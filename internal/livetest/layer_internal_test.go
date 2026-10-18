// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/evidence"
)

func sampleLayer() Layer {
	return Layer{
		ServerURL:    "https://forgejo.compose.forgelab:8443",
		Repository:   "forgeci/fci-layer-artifact",
		Commit:       strings.Repeat("a", 40),
		BinarySHA256: strings.Repeat("b", 64),
	}
}

// layerFixtureFacts stands in for the facts the live scenarios render beyond
// the layer's coordinates. A fixture that needs one not listed here fails
// TestLayerFixtures_RenderToWorkflowsThatPinTheLayer until it is.
func layerFixtureFacts() []string {
	return []string{
		"RENDER_ACCEPTANCE_REPOSITORY", "forgeci/fci-layer-consumer",
		"RENDER_SIGNER_IMAGE", "forgejo.compose.forgelab:8443/forgeci/fci-layer-signer@sha256:" + unrunDigest,
		"RENDER_COSIGN_PUBLIC_KEY_SHA256", unrunDigest,
		"RENDER_COSIGN_PUBLIC_KEY", "-----BEGIN PUBLIC KEY-----\n        AAAA\n        -----END PUBLIC KEY-----",
		"RENDER_RELEASE_GIT_URL", "ssh://git@forgejo.compose.forgelab:2222/forgeci/fci-layer-minimal-release.git",
		"RENDER_RELEASE_GIT_HOST_KEY_FINGERPRINT", "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"RENDER_BASE_REPOSITORY", "forgejo.compose.forgelab:8443/forgeci/fci-layer-registry-smoke-base",
		"RENDER_PLATFORM", "linux/amd64",
	}
}

func TestSignerImage_IsADigestPinnedLabImageOrASkip(t *testing.T) {
	for name, test := range map[string]struct {
		value            string
		want             bool
		skipped, refused bool
	}{
		"unset":            {skipped: true},
		"a lab image":      {value: "forgejo.compose.forgelab:8443/forgeci/fci-layer-signer@sha256:" + unrunDigest, want: true},
		"a tag":            {value: "forgejo.compose.forgelab:8443/forgeci/fci-layer-signer:latest", refused: true},
		"a public host":    {value: "ghcr.io/owner/signer@sha256:" + unrunDigest, refused: true},
		"a short digest":   {value: "forgejo.compose.forgelab:8443/forgeci/fci-layer-signer@sha256:abc", refused: true},
		"trailing content": {value: "forgejo.compose.forgelab:8443/forgeci/fci-layer-signer@sha256:" + unrunDigest + " x", refused: true},
	} {
		t.Setenv(signerImageEnv, test.value)

		tb := &recordingTB{}

		image, ok := SignerImage(tb)
		if (tb.fatal != "") != test.refused || (len(tb.logs) == 1) != test.skipped {
			t.Errorf("%s: fatal %q, logs %q", name, tb.fatal, tb.logs)
		}

		if !test.refused && (ok != test.want || test.want && image != test.value) {
			t.Errorf("%s: SignerImage = %q, %t", name, image, ok)
		}

		if test.skipped && !strings.Contains(tb.logs[0], "just test-live-signer-image") {
			t.Errorf("%s: the skip does not name the recipe: %q", name, tb.logs)
		}
	}
}

func TestLayerRender_FillsTheLayerCoordinatesAndTheRunFacts(t *testing.T) {
	t.Parallel()

	fixture := `env:
  SERVER: RENDER_ACCEPTANCE_SERVER_URL
  REPOSITORY: RENDER_ACCEPTANCE_REPOSITORY
jobs:
  call:
    uses: OWNER/forge-ci/.forgejo/workflows/prepare-release.yml@PIN_FORGE_CI_SHA
    with:
      forge-ci-repository: RENDER_FORGE_CI_REPOSITORY
      signer-image: RENDER_SIGNER_IMAGE
      key-sha256: RENDER_KEY_SHA256
      key: RENDER_KEY
  step:
    steps:
      - uses: https://FORGE_HOST/OWNER/forge-ci/.forgejo/actions/setup-forge-ci@PIN_FORGE_CI_SHA
        with:
          expected-sha256: RENDER_FORGE_CI_SHA256
`

	want := `env:
  SERVER: https://forgejo.compose.forgelab:8443
  REPOSITORY: forgeci/fci-layer-consumer
jobs:
  call:
    uses: forgeci/fci-layer-artifact/.forgejo/workflows/prepare-release.yml@` + strings.Repeat("a", 40) + `
    with:
      forge-ci-repository: forgeci/fci-layer-artifact
      signer-image: forge.example/forgeci/signer@sha256:1
      key-sha256: 2
      key: 3
  step:
    steps:
      - uses: https://forgejo.compose.forgelab:8443/forgeci/fci-layer-artifact/.forgejo/actions/setup-forge-ci@` + strings.Repeat("a", 40) + `
        with:
          expected-sha256: ` + strings.Repeat("b", 64) + `
`

	// A scenario's own facts, the longer of two overlapping placeholders first.
	got, err := sampleLayer().Render(fixture, "RENDER_ACCEPTANCE_REPOSITORY", "forgeci/fci-layer-consumer",
		"RENDER_SIGNER_IMAGE", "forge.example/forgeci/signer@sha256:1", "RENDER_KEY_SHA256", "2", "RENDER_KEY", "3")
	if err != nil {
		t.Fatal(err)
	}

	if got != want {
		t.Errorf("rendered fixture:\n%s\nwant:\n%s", got, want)
	}
}

func TestLayerRender_RefusesAFixtureThatWouldNotRunTheLayer(t *testing.T) {
	t.Parallel()

	for name, fixture := range map[string]string{
		"no pin":                    "uses: https://FORGE_HOST/OWNER/forge-ci/.forgejo/actions/setup-forge-ci@main\n",
		"unknown render fact":       "pin: PIN_FORGE_CI_SHA\nvalue: RENDER_SIGNER_IMAGE\n",
		"another repository's host": "pin: PIN_FORGE_CI_SHA\nuses: https://FORGE_HOST/someone/other/action@v1\n",
		"another owner placeholder": "pin: PIN_FORGE_CI_SHA\nuses: OWNER/other/.forgejo/workflows/x.yml@v1\n",
		"another pin placeholder":   "pin: PIN_FORGE_CI_SHA\nimage: example@PIN_IMAGE_DIGEST\n",
	} {
		rendered, err := sampleLayer().Render(fixture)
		if !errors.Is(err, errs.ErrValidation) || rendered != "" {
			t.Errorf("%s: Render = %q, %v; want a validation refusal and no text", name, rendered, err)
		}
	}

	if rendered, err := sampleLayer().Render("pin: PIN_FORGE_CI_SHA\n", "RENDER_HALF_A_PAIR"); !errors.Is(err, errs.ErrValidation) || rendered != "" {
		t.Errorf("an odd number of facts: Render = %q, %v; want a validation refusal", rendered, err)
	}
}

// TestLayerFixtures_RenderToWorkflowsThatPinTheLayer renders every fixture the
// live scenarios embed, so a placeholder nobody renders, or a rendering that
// is not YAML, is found here and not by a forge that reports it as a workflow
// which never started.
func TestLayerFixtures_RenderToWorkflowsThatPinTheLayer(t *testing.T) {
	t.Parallel()

	fixtures, err := filepath.Glob(filepath.Join("conformance", "testdata", "layer", "*.yml"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no layer fixtures found: %v", err)
	}

	layer := sampleLayer()

	for _, path := range fixtures {
		content, err := os.ReadFile(path) //nolint:gosec // A fixture of this package, found by the glob above.
		if err != nil {
			t.Fatal(err)
		}

		rendered, err := layer.Render(string(content), layerFixtureFacts()...)
		if err != nil {
			t.Errorf("%s: %v", path, err)

			continue
		}

		var workflow struct {
			On   map[string]any `yaml:"on"`
			Jobs map[string]any `yaml:"jobs"`
		}

		if err := yaml.Unmarshal([]byte(rendered), &workflow); err != nil {
			t.Errorf("%s: the rendering is not YAML: %v", path, err)
		}

		// A scenario starts its fixture with a commit, a tag or a pull
		// request, so a fixture with another trigger never runs.
		_, pushed := workflow.On["push"]
		_, requested := workflow.On["pull_request"]

		if !pushed && !requested || len(workflow.Jobs) == 0 {
			t.Errorf("%s: want jobs started by a push or a pull request, got triggers %v and %d jobs", path, workflow.On, len(workflow.Jobs))
		}

		if !strings.Contains(rendered, layer.ServerURL+"/"+layer.Repository+"/.forgejo/") &&
			!strings.Contains(rendered, " "+layer.Repository+"/.forgejo/workflows/") {
			t.Errorf("%s: the rendering references nothing of the served layer", path)
		}
	}
}

// TestLayerRenderReleaseExample_IsAWorkflowTheIsolationGateCanRead renders the
// example this repository ships. A placeholder the example gains, or one whose
// spelling moves, fails here and not in a job on the lab.
func TestLayerRenderReleaseExample_IsAWorkflowTheIsolationGateCanRead(t *testing.T) {
	t.Parallel()

	example, err := os.ReadFile(filepath.Join("..", "..", "examples", "go-cli", "forgejo", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}

	layer := sampleLayer()

	rendered, err := layer.RenderReleaseExample(string(example))
	if err != nil {
		t.Fatal(err)
	}

	if err := yaml.Unmarshal([]byte(rendered), new(map[string]any)); err != nil {
		t.Fatalf("the rendering is not YAML: %v", err)
	}

	// What the gate holds: one pin of one repository, the helper checkout at
	// the same commit, and the two trust inputs as literals of their shape.
	for _, want := range []string{
		"uses: forgeci/fci-layer-artifact/.forgejo/workflows/sign-and-publish-release.yml@" + layer.Commit + "\n",
		"uses: https://forgejo.compose.forgelab:8443/forgeci/fci-layer-artifact/.forgejo/actions/upload-dist@" + layer.Commit + "\n",
		"forge-ci-repository: forgeci/fci-layer-artifact\n",
		"forge-ci-sha: " + layer.Commit + "\n",
		"signer-image: forgejo.compose.forgelab:8443/forgeci/fci-layer-artifact-signer@sha256:" + unrunDigest + "\n",
		"cosign-public-key-sha256: " + unrunDigest + "\n",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the rendered example lacks %q", want)
		}
	}
}

func TestLayerGitEnvironment_ScopesTheCredentialToTheLabGitRoot(t *testing.T) {
	t.Parallel()

	//nolint:gosec // A synthetic credential.
	env := layerGitEnvironment("https://forgejo.compose.forgelab:8443", "forgeci", "synthetic-token")

	if got, want := env["GIT_CONFIG_KEY_0"], "http.https://forgejo.compose.forgelab:8443/.extraHeader"; got != want {
		t.Errorf("credential header key = %q, want %q so git sends it to no other host", got, want)
	}

	pair := base64.StdEncoding.EncodeToString([]byte("forgeci:synthetic-token"))
	if env["GIT_CONFIG_VALUE_0"] != "Authorization: Basic "+pair {
		t.Error("credential header is not the Basic pair of the contract's credential")
	}

	if env["GIT_CONFIG_COUNT"] != "1" || env["GIT_CONFIG_GLOBAL"] != os.DevNull || env["GIT_CONFIG_NOSYSTEM"] != "1" ||
		env["GIT_TERMINAL_PROMPT"] != "0" {
		t.Error("git may read configuration, or prompt, beyond what the kit sets")
	}

	// The snapshot commit is its tree plus these six values, so a missing one
	// lets the operator's clock or name into the pin.
	for _, key := range []string{
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_AUTHOR_DATE",
		"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "GIT_COMMITTER_DATE",
	} {
		if env[key] == "" {
			t.Errorf("%s is unset, so one tree would not always yield one pin", key)
		}
	}

	// Every credential form the kit redacts covers the header, so git echoing
	// its configuration cannot put the token in a test log.
	target := Target{CredentialUsername: "forgeci", Token: "synthetic-token"}
	if redacted := redactTargetSecrets(target, env["GIT_CONFIG_VALUE_0"]); strings.Contains(redacted, pair) {
		t.Error("the credential header survives redaction")
	}
}

// newLayerCheckout is a throwaway repository with a layer directory and one
// file beside it, committed, and the git runner PushLayer would use on it.
func newLayerCheckout(t *testing.T) (layerGit, string) {
	t.Helper()

	settings := layerGitEnvironment("https://forgejo.compose.forgelab:8443", "forgeci", "synthetic-token")

	env := make([]string, 0, len(settings)+2)
	env = append(env, "PATH="+os.Getenv("PATH"), "HOME="+t.TempDir())

	for key, value := range settings {
		env = append(env, key+"="+value)
	}

	git := layerGit{env: env}
	root := t.TempDir()

	writeLayerFile(t, root, layerChecksum, "binary\n")
	writeLayerFile(t, root, "README.md", "beside the layer\n")

	for _, args := range [][]string{
		{"init", "--quiet", "--initial-branch=main"},
		{"add", "--all"},
		{"commit", "--quiet", "--message", "seed"},
		{"commit", "--quiet", "--allow-empty", "--message", "history the forge should not receive"},
	} {
		if _, err := git.run(t.Context(), root, args...); err != nil {
			t.Fatal(err)
		}
	}

	return git, root
}

func writeLayerFile(t *testing.T, root, name, content string) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLayerSnapshot_IsOneParentlessCommitOfHEADsTree(t *testing.T) {
	t.Parallel()

	git, root := newLayerCheckout(t)

	commit, err := git.snapshot(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}

	again, err := git.snapshot(t.Context(), root)
	if err != nil || again != commit {
		t.Errorf("a second snapshot of the same tree is %q, %v; want the same pin %q", again, err, commit)
	}

	if got, want := mustLayerGit(t, git, root, "rev-parse", commit+"^{tree}"), mustLayerGit(t, git, root, "rev-parse", "HEAD^{tree}"); got != want {
		t.Errorf("the snapshot holds tree %s, want HEAD's %s", got, want)
	}

	if reached := mustLayerGit(t, git, root, "rev-list", "--count", commit); reached != "1" {
		t.Errorf("the snapshot reaches %s commits, want 1: the history would be pushed with it", reached)
	}

	// The push is the one PushLayer makes, to a local remote: the branch a
	// fixture pins must hold the snapshot and nothing else.
	remote := t.TempDir()
	if _, err := git.run(t.Context(), remote, "init", "--quiet", "--bare"); err != nil {
		t.Fatal(err)
	}

	if err := git.push(t.Context(), root, remote, commit, layerBranch); err != nil {
		t.Fatal(err)
	}

	if served := mustLayerGit(t, git, remote, "rev-parse", "refs/heads/"+layerBranch); served != commit {
		t.Errorf("the remote serves %s as %s, want %s", served, layerBranch, commit)
	}
}

func TestLayerSnapshot_RefusesALayerThatDiffersFromHEAD(t *testing.T) {
	t.Parallel()

	for name, change := range map[string]string{
		"modified layer file":  layerChecksum,
		"untracked layer file": layerDir + "/actions/new/action.yml",
	} {
		git, root := newLayerCheckout(t)
		writeLayerFile(t, root, change, "uncommitted\n")

		commit, err := git.snapshot(t.Context(), root)
		if !errors.Is(err, errs.ErrValidation) || commit != "" || !strings.Contains(err.Error(), change) {
			t.Errorf("%s: snapshot = %q, %v; want a refusal that names %s", name, commit, err, change)
		}
	}

	// Nothing a job fetches lives outside the layer directory, so a scenario
	// under development does not have to be committed to be run.
	git, root := newLayerCheckout(t)
	writeLayerFile(t, root, "README.md", "edited beside the layer\n")

	if _, err := git.snapshot(t.Context(), root); err != nil {
		t.Errorf("a change outside %s refused the snapshot: %v", layerDir, err)
	}
}

func mustLayerGit(t *testing.T, git layerGit, dir string, args ...string) string {
	t.Helper()

	out, err := git.run(t.Context(), dir, args...)
	if err != nil {
		t.Fatal(err)
	}

	return out
}

func TestImageBuilds_AreExpectedUnlessTheOperatorSaysOtherwise(t *testing.T) {
	for value, want := range map[string]struct{ builds, skipped, refused bool }{
		"":    {builds: true},
		"1":   {builds: true},
		"0":   {skipped: true},
		"yes": {refused: true},
	} {
		t.Setenv(imageBuildsEnv, value)

		tb := &recordingTB{}

		if got := ImageBuilds(tb); got != want.builds || (tb.fatal != "") != want.refused || (len(tb.logs) == 1) != want.skipped {
			t.Errorf("%s=%q: ImageBuilds = %t, fatal %q, logs %q", imageBuildsEnv, value, got, tb.fatal, tb.logs)
		}

		if want.skipped && !strings.Contains(tb.logs[0], "k3s-full-release") {
			t.Errorf("the skip does not say where the scenario runs: %q", tb.logs)
		}
	}
}

// A request tag signed by the kit must be one git verifies against the line
// the kit hands the consumer: that pair is all the layer is given.
func TestRequestSigner_SignsATagItsAllowedSignersLineVerifies(t *testing.T) {
	t.Parallel()
	evidence.RequireTool(t, "ssh-keygen")

	git, root := newLayerCheckout(t)
	signer := NewRequestSigner(t, t.TempDir())

	if err := signer.tag(t.Context(), git, root, "release-request/v0.0.1", "HEAD", "Request release v0.0.1"); err != nil {
		t.Fatal(err)
	}

	if kind := mustLayerGit(t, git, root, "cat-file", "-t", "refs/tags/release-request/v0.0.1"); kind != "tag" {
		t.Errorf("the request is a %s, want an annotated tag", kind)
	}

	if tagger := mustLayerGit(t, git, root, "for-each-ref", "--format=%(taggeremail)", "refs/tags/release-request/v0.0.1"); tagger != "<"+requestSigner+">" {
		t.Errorf("the tagger is %s, want the principal %s of the allowed-signers line", tagger, requestSigner)
	}

	allowed := filepath.Join(t.TempDir(), "allowed-signers")
	if err := os.WriteFile(allowed, []byte(signer.AllowedSigners), 0o600); err != nil {
		t.Fatal(err)
	}

	verify := func(file string) error {
		_, err := git.run(t.Context(), root, "-c", "gpg.format=ssh", "-c", "gpg.ssh.allowedSignersFile="+file, "verify-tag", "release-request/v0.0.1")

		return err
	}

	if err := verify(allowed); err != nil {
		t.Errorf("git does not verify the tag against the signer's own line: %v", err)
	}

	other := NewRequestSigner(t, t.TempDir())
	if err := os.WriteFile(allowed, []byte(other.AllowedSigners), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := verify(allowed); err == nil {
		t.Error("git verifies the tag against another signer's line")
	}
}

// A push is retried only when the connection broke: the forge's own refusal
// would come back the same way every time.
func TestBrokenConnection_RetriesTransportFailuresOnly(t *testing.T) {
	t.Parallel()

	for message, broken := range map[string]bool{
		"fatal: unable to access 'https://gitlab.compose.forgelab:8443/demo/demo-hello-maven.git/': GnuTLS recv error (-110): The TLS connection was non-properly terminated.": true,
		"error: RPC failed; curl 56 Recv failure: Connection reset by peer":                                                                                                    true,
		"fatal: unable to access 'https://forge.invalid/x.git/': The requested URL returned error: 503":                                                                        true,
		"fatal: unable to access 'https://forge.invalid/x.git/': The requested URL returned error: 403":                                                                        false,
		" ! [rejected]        main -> main (non-fast-forward)":                                                                                                                 false,
		" ! [remote rejected] refs/tags/v1.0.0 -> refs/tags/v1.0.0 (pre-receive hook declined)":                                                                                false,
		"fatal: 'refs/tags/missing' does not appear to be a git repository":                                                                                                    false,
	} {
		if got := brokenConnection(message); got != broken {
			t.Errorf("brokenConnection(%q) = %v, want %v", message, got, broken)
		}
	}
}

// A commit on a consumer's own history is dated when it is made; everything
// else in git's closed environment, the credential header included, stays.
func TestLayerGitDated_ReplacesOnlyTheCommitDates(t *testing.T) {
	t.Parallel()

	fixed := layerGit{env: []string{"GIT_AUTHOR_DATE=2000-01-01T00:00:00Z", "GIT_CONFIG_VALUE_0=Authorization: Basic x", "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z"}}
	when := time.Date(2026, 10, 16, 2, 10, 12, 0, time.UTC)

	got := fixed.dated(when).env
	want := []string{"GIT_CONFIG_VALUE_0=Authorization: Basic x", "GIT_AUTHOR_DATE=2026-10-16T02:10:12Z", "GIT_COMMITTER_DATE=2026-10-16T02:10:12Z"}

	if !slices.Equal(got, want) {
		t.Fatalf("dated().env = %q, want %q", got, want)
	}

	if fixed.env[0] != "GIT_AUTHOR_DATE=2000-01-01T00:00:00Z" {
		t.Fatal("dated changed the receiver's environment")
	}
}
