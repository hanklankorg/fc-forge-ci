// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"codefloe.com/itiquette/forge-lab-contract/labcontract"
	"github.com/goccy/go-yaml"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/evidence"
)

// What the consumers under testdata/consumer pinned when they were copied.
const (
	consumerPinned       = "b369db7d70af68fa93a4b404841c4ce2ddbf90c0"
	consumerPinnedBinary = "f43d7a4ac43046a5fe3aac36259edec1f1361a3e076fa75d6aef76b61fc1227b"
	consumerSignerImage  = "codefloe.com/itiquette/forge-ci-signer@sha256:9a51d9f761cd269cadb2066710c9d2dd293f4e1020e229ff4f47ee1e6bce6234"
	consumerKeyDigest    = "0a118a651aac6f751385aeaeb4242996ecc5b1b30c272d8da4a75369abd6b630" // gitleaks:allow -- public-key digest, not a credential.

	labSignerImage = "forgejo.compose.forgelab:8443/forgeci/forge-ci-live-signer@sha256:" + unrunDigest
	labGitURL      = "ssh://git@forgejo:2222/forgeci/fci-consumer-trial.git"
	labFingerprint = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	labPublicKey   = "-----BEGIN PUBLIC KEY-----\nAAAA\n-----END PUBLIC KEY-----\n"
)

// consumerWorkflow reads a real consumer's workflow, copied as it was.
func consumerWorkflow(t *testing.T, name string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join("testdata", "consumer", name))
	if err != nil {
		t.Fatal(err)
	}

	return string(content)
}

func releaseFacts() ConsumerFacts {
	return ConsumerFacts{
		Inputs: map[string]string{
			"forge-ci-repository":              "forgeci/fci-layer-artifact",
			"signer-image":                     labSignerImage,
			"cosign-public-key":                labPublicKey,
			"cosign-public-key-sha256":         strings.Repeat("c", 64),
			"release-git-url":                  labGitURL,
			"release-git-host-key-fingerprint": labFingerprint,
		},
		Secrets: map[string]string{
			"RELEASE_SSH_SIGNING_KEY": "ssh", "RELEASE_COSIGN_PRIVATE_KEY": "cosign", "RELEASE_COSIGN_PASSPHRASE": "pass",
			"RELEASE_GPG_PRIVATE_KEY": "gpg", "RELEASE_GPG_FINGERPRINT": "fingerprint", "RELEASE_GPG_PASSPHRASE": "pass",
			"RELEASE_TOKEN": "token", "RELEASE_REGISTRY_TOKEN": "token",
		},
	}
}

func TestParseTrialSettings_ReadsTheCallersWords(t *testing.T) {
	t.Parallel()

	settings, err := ParseTrialSettings(TrialPullRequest, nil)
	if err != nil || settings.Workflow != trialPullRequestWorkflow || settings.Version != "" || len(settings.Variables) != 0 || settings.TokenSecrets != nil {
		t.Errorf("a pull-request trial without settings = %+v, %v", settings, err)
	}

	settings, err = ParseTrialSettings(TrialRelease, []string{
		"workflow=ship.yaml", "version=1.20.3", "token=PACKAGES_TOKEN", "token=PAGES_TOKEN",
		"CI_JOB_IMAGE=forge.example/forgeci/job@sha256:1", "NOTE=two words", "APPROVED=@layer-commit", "CANDIDATE=@request-commit",
		"interrupt-after=Build go flavor image", "secret-file=DEPENDENCY_GITHUB_TOKEN:/home/lab/github.pat",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := TrialSettings{
		Event: TrialRelease, Workflow: "ship.yaml", Version: "1.20.3", TokenSecrets: []string{"PACKAGES_TOKEN", "PAGES_TOKEN"},
		Variables: map[string]string{
			"CI_JOB_IMAGE": "forge.example/forgeci/job@sha256:1", "NOTE": "two words", "APPROVED": "@layer-commit", "CANDIDATE": "@request-commit",
		},
	}
	if settings.Workflow != want.Workflow || settings.Version != want.Version || !slices.Equal(settings.TokenSecrets, want.TokenSecrets) ||
		!maps.Equal(settings.Variables, want.Variables) || settings.InterruptAfter != "Build go flavor image" ||
		!maps.Equal(settings.FileSecrets, map[string]string{"DEPENDENCY_GITHUB_TOKEN": "/home/lab/github.pat"}) {
		t.Errorf("settings = %+v, want %+v", settings, want)
	}

	resolved := settings.ResolvedVariables("layer", "request")
	if resolved["APPROVED"] != "layer" || resolved["CANDIDATE"] != "request" || resolved["NOTE"] != "two words" || len(resolved) != 4 {
		t.Errorf("resolved variables = %v", resolved)
	}

	if defaults, _ := ParseTrialSettings(TrialRelease, nil); defaults.Workflow != trialReleaseWorkflow || defaults.Version != "0.0.1" {
		t.Errorf("a release trial without settings = %+v", defaults)
	}
}

func TestConsumerTrialLayerURL_IsTheForgejoScratchLayer(t *testing.T) {
	t.Parallel()

	lab := &labcontract.Contract{Endpoints: []labcontract.Endpoint{
		{Name: "gitlab", Kind: "gitlab", WebBaseURL: "https://gitlab.lab.test/", Credential: labcontract.Credential{Username: "other"}},
		{Name: "forgejo", Kind: "forgejo", WebBaseURL: "https://forgejo.lab.test/", Credential: labcontract.Credential{Username: "forgeci"}},
	}}

	got, err := consumerTrialLayerURL(lab)
	if want := "https://forgejo.lab.test/forgeci/fci-consumer-trial-layer"; err != nil || got != want {
		t.Errorf("consumerTrialLayerURL() = %q, %v; want %q", got, err, want)
	}

	if _, err := consumerTrialLayerURL(&labcontract.Contract{}); err == nil {
		t.Error("a contract without a Forgejo endpoint was accepted")
	}
}

func TestParseTrialSettings_RefusesWhatItCannotMean(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		event string
		words []string
	}{
		"unknown event":                    {event: "push"},
		"no value":                         {event: TrialRelease, words: []string{"CI_JOB_IMAGE="}},
		"no key":                           {event: TrialRelease, words: []string{"pullrequest.yml"}},
		"lowercase variable":               {event: TrialRelease, words: []string{"ci_job_image=x"}},
		"workflow outside directory":       {event: TrialRelease, words: []string{"workflow=../release.yml"}},
		"workflow not YAML":                {event: TrialRelease, words: []string{"workflow=release.sh"}},
		"version with a v and more":        {event: TrialRelease, words: []string{"version=vv1.2.3"}},
		"prerelease version":               {event: TrialRelease, words: []string{"version=1.2.3-rc.1"}},
		"version for a pull request":       {event: TrialPullRequest, words: []string{"version=1.2.3"}},
		"token named twice":                {event: TrialRelease, words: []string{"token=A", "token=A"}},
		"lowercase token":                  {event: TrialRelease, words: []string{"token=packages"}},
		"token with a value":               {event: TrialRelease, words: []string{"token=A=secret"}},
		"secret file twice":                {event: TrialRelease, words: []string{"secret-file=A:/a", "secret-file=A:/b"}},
		"secret file and token":            {event: TrialRelease, words: []string{"token=A", "secret-file=A:/a"}},
		"token and secret file":            {event: TrialRelease, words: []string{"secret-file=A:/a", "token=A"}},
		"relative secret file":             {event: TrialRelease, words: []string{"secret-file=A:a.pat"}},
		"secret file without a path":       {event: TrialRelease, words: []string{"secret-file=A"}},
		"lowercase secret file":            {event: TrialRelease, words: []string{"secret-file=a:/a"}},
		"variable twice":                   {event: TrialRelease, words: []string{"A=1", "A=2"}},
		"unknown reference":                {event: TrialRelease, words: []string{"A=@layer"}},
		"value of two lines":               {event: TrialRelease, words: []string{"A=one\ntwo"}},
		"interrupt of a pull request":      {event: TrialPullRequest, words: []string{"interrupt-after=Build"}},
		"interrupt named twice":            {event: TrialRelease, words: []string{"interrupt-after=Build", "interrupt-after=Sign"}},
		"interrupt with spaces round":      {event: TrialRelease, words: []string{"interrupt-after= Build"}},
		"second version on pull request":   {event: TrialPullRequest, words: []string{"next-version=1.2.4"}},
		"second version does not increase": {event: TrialRelease, words: []string{"version=1.2.3", "next-version=1.2.3"}},
		"second version goes backwards":    {event: TrialRelease, words: []string{"version=1.2.3", "next-version=1.2.2"}},
		"second version named twice":       {event: TrialRelease, words: []string{"next-version=1.2.3", "next-version=1.2.4"}},
		"second version with interruption": {event: TrialRelease, words: []string{"next-version=1.2.3", "interrupt-after=Build"}},
	} {
		if settings, err := ParseTrialSettings(test.event, test.words); !errors.Is(err, errs.ErrValidation) {
			t.Errorf("%s: ParseTrialSettings = %+v, %v; want a validation refusal", name, settings, err)
		}
	}
}

func TestReadConsumerPin_ReadsRealConsumers(t *testing.T) {
	t.Parallel()

	const (
		actions   = "https://codefloe.com/itiquette/forge-ci/.forgejo/actions/"
		workflows = "itiquette/forge-ci/.forgejo/workflows/"
	)

	pin, err := ReadConsumerPin(map[string]string{
		"pullrequest.yml": consumerWorkflow(t, "gommitlint-pullrequest.yml"),
		"release.yml":     consumerWorkflow(t, "gommitlint-release.yml"),
	})
	if want := (ConsumerPin{Actions: actions, Workflows: workflows, Commit: consumerPinned}); err != nil || pin != want {
		t.Errorf("Gommitlint's pin = %+v, %v; want %+v", pin, err, want)
	}

	// Nanolinter passes the binary's digest as a literal, and calls no
	// workflow of the layer from its pull request.
	pin, err = ReadConsumerPin(map[string]string{"pullrequest.yml": consumerWorkflow(t, "nanolinter-pullrequest.yml")})
	if want := (ConsumerPin{Actions: actions, Commit: consumerPinned, BinarySHA256: consumerPinnedBinary}); err != nil || pin != want {
		t.Errorf("Nanolinter's pin = %+v, %v; want %+v", pin, err, want)
	}
}

func TestReadConsumerPin_RefusesAConsumerItCannotStandInFor(t *testing.T) {
	t.Parallel()

	const use = "      - uses: https://forge.example/owner/forge-ci/.forgejo/actions/setup-forge-ci@"

	for name, workflows := range map[string]map[string]string{
		"no layer reference": {"ci.yml": "jobs:\n  test:\n    steps:\n      - uses: https://forge.example/actions/checkout@v4\n"},
		"a branch for a pin": {"ci.yml": use + "main\n"},
		"two commits":        {"a.yml": use + strings.Repeat("1", 40) + "\n", "b.yml": use + strings.Repeat("2", 40) + "\n"},
		"two repositories": {"ci.yml": use + strings.Repeat("1", 40) + "\n" +
			"      - uses: https://forge.example/other/forge-ci/.forgejo/actions/build-go@" + strings.Repeat("1", 40) + "\n"},
		"two binaries": {"ci.yml": use + strings.Repeat("1", 40) + "\n        with:\n          expected-sha256: " + strings.Repeat("a", 64) +
			"\n" + use + strings.Repeat("1", 40) + "\n        with:\n          expected-sha256: " + strings.Repeat("b", 64) + "\n"},
	} {
		if pin, err := ReadConsumerPin(workflows); !errors.Is(err, errs.ErrValidation) {
			t.Errorf("%s: ReadConsumerPin = %+v, %v; want a validation refusal", name, pin, err)
		}
	}
}

// TestPlanConsumer_RendersARealRelease plans Gommitlint's release as the
// trial does, and holds the rendering to what a job would fetch and read.
func TestPlanConsumer_RendersARealRelease(t *testing.T) {
	t.Parallel()

	layer := sampleLayer()
	source := consumerWorkflow(t, "gommitlint-release.yml")

	plan, err := layer.PlanConsumer(map[string]string{
		"pullrequest.yml": consumerWorkflow(t, "gommitlint-pullrequest.yml"), "release.yml": source,
	}, releaseFacts())
	if err != nil {
		t.Fatal(err)
	}

	rendered := plan.Render(source)

	if err := yaml.Unmarshal([]byte(rendered), new(map[string]any)); err != nil {
		t.Fatalf("the rendering is not YAML: %v", err)
	}

	for _, want := range []string{
		"uses: forgeci/fci-layer-artifact/.forgejo/workflows/prepare-release.yml@" + layer.Commit + "\n",
		"uses: forgeci/fci-layer-artifact/.forgejo/workflows/sign-and-publish-release.yml@" + layer.Commit + "\n",
		"uses: https://forgejo.compose.forgelab:8443/forgeci/fci-layer-artifact/.forgejo/actions/record-release-image@" + layer.Commit + "\n",
		"      forge-ci-repository: forgeci/fci-layer-artifact\n",
		"      forge-ci-sha: " + layer.Commit + "\n",
		"      signer-image: " + labSignerImage + "\n",
		"      cosign-public-key-sha256: " + strings.Repeat("c", 64) + "\n",
		// What comes from a variable or a secret is left as the consumer wrote it.
		"cosign-public-key: ${{ vars.COSIGN_PUBLIC_KEY }}\n",
		"release-git-url: ${{ vars.RELEASE_GIT_URL }}\n",
		"RELEASE_TOKEN: ${{ secrets.RELEASE_API_TOKEN }}\n",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the rendered release lacks %q", want)
		}
	}

	// A comment may still name the organization's repository; nothing that is
	// fetched or compared does.
	for _, gone := range []string{
		"itiquette/forge-ci/.forgejo", "forge-ci-repository: itiquette/forge-ci", consumerPinned, consumerSignerImage, consumerKeyDigest,
	} {
		if strings.Contains(rendered, gone) {
			t.Errorf("the rendered release still holds %q", gone)
		}
	}

	if want := map[string]string{
		"COSIGN_PUBLIC_KEY": labPublicKey, "RELEASE_GIT_URL": labGitURL, "RELEASE_GIT_HOST_KEY_FINGERPRINT": labFingerprint,
	}; !maps.Equal(plan.Variables, want) {
		t.Errorf("variables = %v, want %v", plan.Variables, want)
	}

	// The layer's secret names, under the names the consumer reads them by.
	if want := map[string]string{
		"RELEASE_SSH_SIGNING_KEY": "ssh", "RELEASE_COSIGN_PRIVATE_KEY": "cosign", "RELEASE_COSIGN_PASSPHRASE": "pass",
		"RELEASE_GPG_PRIVATE_KEY": "gpg", "RELEASE_GPG_FINGERPRINT": "fingerprint", "RELEASE_GPG_PASSPHRASE": "pass",
		"RELEASE_API_TOKEN": "token", "PACKAGE_PUBLISH_TOKEN": "token",
	}; !maps.Equal(plan.Secrets, want) {
		t.Errorf("secrets = %v, want %v", slices.Sorted(maps.Keys(plan.Secrets)), slices.Sorted(maps.Keys(want)))
	}

	// A consumer's policy test holds the same literals, and its Go code may
	// import a module whose path only looks like the layer's.
	policy := `	layerSetupAction = "https://codefloe.com/itiquette/forge-ci/.forgejo/actions/setup-forge-ci@` + consumerPinned + `"
	layerPrepare     = "itiquette/forge-ci/.forgejo/workflows/prepare-release.yml@` + consumerPinned + `"
	signerImage      = "` + consumerSignerImage + `"
	trustRootDigest  = "` + consumerKeyDigest + `"
	import "codefloe.com/itiquette/forge-ci/pkg"
`

	wantPolicy := `	layerSetupAction = "https://forgejo.compose.forgelab:8443/forgeci/fci-layer-artifact/.forgejo/actions/setup-forge-ci@` + layer.Commit + `"
	layerPrepare     = "forgeci/fci-layer-artifact/.forgejo/workflows/prepare-release.yml@` + layer.Commit + `"
	signerImage      = "` + labSignerImage + `"
	trustRootDigest  = "` + strings.Repeat("c", 64) + `"
	import "codefloe.com/itiquette/forge-ci/pkg"
`
	if got := plan.Render(policy); got != wantPolicy {
		t.Errorf("a policy test renders to:\n%s\nwant:\n%s", got, wantPolicy)
	}
}

// A pull-request trial brings no release facts, so the consumer's signer
// image and key digest stay: its isolation gate reads them as literals and
// nothing runs in them.
func TestPlanConsumer_APullRequestLeavesTheReleaseTrustAlone(t *testing.T) {
	t.Parallel()

	layer := sampleLayer()
	release := consumerWorkflow(t, "gommitlint-release.yml")

	plan, err := layer.PlanConsumer(map[string]string{"pullrequest.yml": consumerWorkflow(t, "gommitlint-pullrequest.yml"), "release.yml": release},
		ConsumerFacts{Inputs: map[string]string{"forge-ci-repository": layer.Repository}})
	if err != nil {
		t.Fatal(err)
	}

	rendered := plan.Render(release)
	if !strings.Contains(rendered, "signer-image: "+consumerSignerImage+"\n") || !strings.Contains(rendered, "cosign-public-key-sha256: "+consumerKeyDigest+"\n") ||
		strings.Contains(rendered, consumerPinned) || len(plan.Variables) != 0 || len(plan.Secrets) != 0 {
		t.Errorf("a pull-request plan changed the release trust, kept the pin, or set something: variables %v, secrets %d", plan.Variables, len(plan.Secrets))
	}
}

// Nanolinter writes the digest of the layer's binary beside every setup. The
// layer under test has another binary, so each of them must follow.
func TestPlanConsumer_ReplacesTheBinaryDigestWhereverItStands(t *testing.T) {
	t.Parallel()

	layer := sampleLayer()
	source := consumerWorkflow(t, "nanolinter-pullrequest.yml")

	plan, err := layer.PlanConsumer(map[string]string{"pullrequest.yml": source}, ConsumerFacts{})
	if err != nil {
		t.Fatal(err)
	}

	rendered := plan.Render(source)

	passed := strings.Count(source, "expected-sha256: "+consumerPinnedBinary)
	if passed == 0 || strings.Count(rendered, "expected-sha256: "+layer.BinarySHA256) != passed || strings.Contains(rendered, consumerPinnedBinary) {
		t.Errorf("the source passes the binary digest %d times, and the rendering has the layer's %d times",
			passed, strings.Count(rendered, "expected-sha256: "+layer.BinarySHA256))
	}
}

func TestPlanConsumer_TakesEachFactTheWayTheConsumerPassesIt(t *testing.T) {
	t.Parallel()

	layer := sampleLayer()

	const call = "jobs:\n  sign:\n    uses: owner/forge-ci/.forgejo/workflows/sign-and-publish-release.yml@" + consumerPinned + "\n    with:\n"

	facts := ConsumerFacts{Inputs: map[string]string{
		"signer-image": labSignerImage, "cosign-public-key": labPublicKey, "release-git-url": labGitURL, "forge-ci-repository": layer.Repository,
	}}

	// A quoted literal, an input left empty, and an expression a formatter folded.
	plan, err := layer.PlanConsumer(map[string]string{"release.yml": call +
		"      signer-image: 'forge.example/owner/signer@sha256:1'\n" +
		"      forge-ci-repository: ''\n" +
		"      release-git-url: \"ssh://git@forge.example/owner/repository.git\"\n" +
		"      cosign-public-key: ${{ vars.ORGANIZATION_KEY\n        }}\n"}, facts)
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{
		"owner/forge-ci/.forgejo/workflows/", "forgeci/fci-layer-artifact/.forgejo/workflows/",
		consumerPinned, layer.Commit,
		`release-git-url: "ssh://git@forge.example/owner/repository.git"`, "release-git-url: " + labGitURL,
		"forge.example/owner/signer@sha256:1", labSignerImage,
	}; !slices.Equal(plan.Replace, want) || !maps.Equal(plan.Variables, map[string]string{"ORGANIZATION_KEY": labPublicKey}) {
		t.Errorf("plan replaces %q and sets %v, want %q and the key as ORGANIZATION_KEY", plan.Replace, plan.Variables, want)
	}

	for name, with := range map[string]string{
		"a key written into the workflow":   "      cosign-public-key: |\n        -----BEGIN PUBLIC KEY-----\n",
		"an expression that is no variable": "      signer-image: ${{ inputs.signer-image }}\n",
		"a variable with a fallback":        "      signer-image: ${{ vars.SIGNER || 'forge.example/owner/signer@sha256:1' }}\n",
		"one variable for two facts":        "      signer-image: ${{ vars.ONE }}\n      release-git-url: ${{ vars.ONE }}\n",
	} {
		if refused, err := layer.PlanConsumer(map[string]string{"release.yml": call + with}, facts); !errors.Is(err, errs.ErrValidation) {
			t.Errorf("%s: PlanConsumer = %+v, %v; want a validation refusal", name, refused, err)
		}
	}
}

// A reference the plan cannot move would be fetched from where the consumer
// points it, so the trial would run a published layer and say it ran this one.
func TestPlanConsumer_RefusesAReferenceLeftPointingAway(t *testing.T) {
	t.Parallel()

	const pinned = "    steps:\n      - uses: https://forge.example/owner/forge-ci/.forgejo/actions/setup-forge-ci@" + consumerPinned + "\n"

	for name, more := range map[string]string{
		"a branch":             "      - uses: https://forge.example/owner/forge-ci/.forgejo/actions/build-go@main\n",
		"a second layer":       "      - uses: https://forge.example/owner/release-ci/.forgejo/actions/build-go@" + strings.Repeat("1", 40) + "\n",
		"a workflow by branch": "  call:\n    uses: owner/forge-ci/.forgejo/workflows/prepare-release.yml@main\n",
	} {
		if plan, err := sampleLayer().PlanConsumer(map[string]string{"ci.yml": pinned + more}, ConsumerFacts{}); !errors.Is(err, errs.ErrValidation) {
			t.Errorf("%s: PlanConsumer = %+v, %v; want a validation refusal", name, plan, err)
		}
	}

	// The consumer's own reusable workflow is its tree's and fetches nothing.
	if _, err := sampleLayer().PlanConsumer(map[string]string{"ci.yml": pinned + "  own:\n    uses: ./.forgejo/workflows/build.yml\n"}, ConsumerFacts{}); err != nil {
		t.Errorf("a local workflow call refused the plan: %v", err)
	}
}

func TestPlanConsumer_SeparatesDefaultedBaseAndFinalRepositories(t *testing.T) {
	t.Parallel()

	const (
		call     = "jobs:\n  images:\n    uses: owner/forge-ci/.forgejo/workflows/release-images.yml@" + consumerPinned + "\n    with:\n"
		base     = "${{ vars.BASE_IMAGE_REPOSITORY || 'ghcr.io/owner/app-base' }}"
		final    = "${{ vars.IMAGE_REPOSITORY || 'ghcr.io/owner/app' }}"
		labFinal = "forgejo.compose.forgelab:8443/forgeci/fci-consumer-trial"
	)

	facts := ConsumerFacts{Inputs: map[string]string{
		"image-repository": labFinal, "publication-image-repository": labFinal,
		"base-repository": labFinal + "-base",
	}}
	workflows := map[string]string{
		"a-build-bases.yml": call + "      image-repository: " + base + "\n",
		"b-release.yml":     call + "      image-repository: " + final + "\n      publication-image-repository: " + final + "\n",
		"z-sign-bases.yml":  call + "      base-repository: " + base + "\n",
	}

	plan, err := sampleLayer().PlanConsumer(workflows, facts)
	if err != nil {
		t.Fatal(err)
	}

	if !maps.Equal(plan.Variables, map[string]string{"BASE_IMAGE_REPOSITORY": labFinal + "-base", "IMAGE_REPOSITORY": labFinal}) {
		t.Fatalf("repository coordinates collapsed or escaped: %v", plan.Variables)
	}

	for name, source := range workflows {
		rendered := plan.Render(source)
		if err := yaml.Unmarshal([]byte(rendered), new(map[string]any)); err != nil {
			t.Fatalf("%s rendered invalid YAML: %v", name, err)
		}

		if strings.Contains(source, "image-repository: "+base) && !strings.Contains(rendered, "image-repository: "+base) {
			t.Fatalf("base forwarding was rewritten to a final destination: %s", rendered)
		}
	}
	// Caller settings cannot replace these bindings: the trial's existing
	// duplicate-source guard rejects even a same-named production coordinate.
	if err := bind(plan.Variables, "variable", "BASE_IMAGE_REPOSITORY", "ghcr.io/production/base"); !errors.Is(err, errs.ErrValidation) {
		t.Fatal("production override admitted")
	}
}

func TestPlanConsumer_RepositoryDefaultsRefuseUnsubstitutableExpressions(t *testing.T) {
	t.Parallel()

	const call = "jobs:\n  images:\n    uses: owner/forge-ci/.forgejo/workflows/release-images.yml@" + consumerPinned + "\n    with:\n"

	facts := ConsumerFacts{Inputs: map[string]string{
		"image-repository": "forgejo.compose.forgelab:8443/forgeci/fci-consumer-trial",
		"base-repository":  "forgejo.compose.forgelab:8443/forgeci/fci-consumer-trial-base",
	}}

	for _, expression := range []string{
		"${{ inputs.image-repository }}",
		"${{ vars.REPO || secrets.REGISTRY }}",
		"${{ vars.REPO || 'ghcr.io/owner/app' || vars.OTHER }}",
		"${{ vars.REPO && 'ghcr.io/owner/app' }}",
		"${{ vars.REPO || 'https://ghcr.io/owner/app' }}",
		"${{ vars.REPO || 'ghcr.io/owner/app:latest' }}",
		"${{ vars.REPO || 'ghcr.io/owner/app?query' }}",
		"${{ format('{0}/app', vars.REGISTRY) }}",
	} {
		for _, input := range []string{"base-repository", "image-repository"} {
			if _, err := sampleLayer().PlanConsumer(map[string]string{"release.yml": call + "      " + input + ": " + expression + "\n"}, facts); !errors.Is(err, errs.ErrValidation) {
				t.Errorf("admitted %s: %s: %v", input, expression, err)
			}
		}
	}
	// A repository default must not make fallback signer/key expressions legal.
	for _, input := range []string{"signer-image", "cosign-public-key"} {
		facts.Inputs[input] = "lab-value"
		if _, err := sampleLayer().PlanConsumer(map[string]string{"release.yml": call + "      " + input + ": ${{ vars.TRUST || 'ghcr.io/owner/app' }}\n"}, facts); !errors.Is(err, errs.ErrValidation) {
			t.Fatalf("admitted defaulted trust input %s", input)
		}
	}
}

// newConsumerCheckout is a throwaway consumer: a workflow that pins the
// layer, a policy file that repeats the pin, an executable, a binary file
// and a link, committed twice so there is history the forge should not get.
func newConsumerCheckout(t *testing.T, objectFormat string) (layerGit, string) {
	t.Helper()

	git, _ := newLayerCheckout(t)
	root := t.TempDir()

	writeLayerFile(t, root, consumerWorkflows+"/ci.yml",
		"jobs:\n  test:\n    steps:\n      - uses: https://forge.example/owner/forge-ci/.forgejo/actions/setup-forge-ci@"+consumerPinned+"\n")
	writeLayerFile(t, root, "policy/pin.sh", "#!/bin/sh\npin="+consumerPinned+"\n")
	writeLayerFile(t, root, "README.md", "no pin here\n")
	writeLayerFile(t, root, "logo.bin", "\x00\x01"+consumerPinned)

	if err := os.Chmod(filepath.Join(root, "policy", "pin.sh"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink("policy/pin.sh", filepath.Join(root, "pin")); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"init", "--quiet", "--initial-branch=main", "--object-format=" + objectFormat},
		{"add", "--all"},
		{"commit", "--quiet", "--message", "seed"},
		{"commit", "--quiet", "--allow-empty", "--message", "history the forge should not receive"},
	} {
		mustLayerGit(t, git, root, args...)
	}

	return git, root
}

const (
	consumerSHA1   = "sha1"
	consumerSHA256 = "sha256"
)

func TestConsumer_StagesTheRenderedTreeWithoutWritingToTheCheckout(t *testing.T) {
	t.Parallel()

	for _, objectFormat := range []string{consumerSHA1, consumerSHA256} {
		git, root := newConsumerCheckout(t, objectFormat)
		before := mustLayerGit(t, git, root, "count-objects", "-v")

		consumer, err := openConsumer(t.Context(), git, root, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}

		if consumer.ObjectFormat != objectFormat || consumer.Commit != mustLayerGit(t, git, root, "rev-parse", "HEAD") {
			t.Errorf("%s: opened %+v", objectFormat, consumer)
		}

		workflows, err := consumer.workflows(t.Context())
		if err != nil || len(workflows) != 1 || !strings.Contains(workflows["ci.yml"], consumerPinned) {
			t.Fatalf("%s: workflows = %v, %v", objectFormat, workflows, err)
		}

		layer := sampleLayer()

		plan, err := layer.PlanConsumer(workflows, ConsumerFacts{})
		if err != nil {
			t.Fatal(err)
		}

		commit, err := consumer.commitRendered(t.Context(), plan)
		if err != nil {
			t.Fatal(err)
		}

		stage := func(args ...string) string { return mustLayerGit(t, git, consumer.stage, args...) }

		if reached := stage("rev-list", "--count", commit); reached != "1" {
			t.Errorf("%s: the snapshot reaches %s commits, want 1", objectFormat, reached)
		}

		// The two text files that held the pin changed, and nothing else: not
		// the binary file that holds it too, not the link, not a mode.
		if changed := stage("diff-tree", "-r", "--name-status", consumer.Commit, commit); changed != "M\t.forgejo/workflows/ci.yml\nM\tpolicy/pin.sh" {
			t.Errorf("%s: the snapshot changes:\n%s", objectFormat, changed)
		}

		if script := stage("cat-file", "blob", commit+":policy/pin.sh"); script != "#!/bin/sh\npin="+layer.Commit {
			t.Errorf("%s: the policy file renders to %q", objectFormat, script)
		}

		if mode := stage("ls-tree", commit, "--", "policy/pin.sh"); !strings.HasPrefix(mode, "100755 ") {
			t.Errorf("%s: the executable is staged as %q", objectFormat, mode)
		}

		// The push a trial makes, to a local remote of the same object format.
		remote := t.TempDir()
		mustLayerGit(t, git, remote, "init", "--quiet", "--bare", "--object-format="+objectFormat)

		if err := git.push(t.Context(), consumer.stage, remote, commit, defaultBranch); err != nil {
			t.Fatal(err)
		}

		if served := mustLayerGit(t, git, remote, "rev-parse", "refs/heads/"+defaultBranch); served != commit {
			t.Errorf("%s: the remote serves %s as main, want %s", objectFormat, served, commit)
		}

		if after := mustLayerGit(t, git, root, "count-objects", "-v"); after != before || mustLayerGit(t, git, root, "status", "--porcelain") != "" {
			t.Errorf("%s: the trial wrote to the checkout: objects before\n%s\nafter\n%s", objectFormat, before, after)
		}
	}
}

func TestConsumer_SnapshotPreservesTheAdmittedTreeAndLeavesHistoryBehind(t *testing.T) {
	t.Parallel()

	for _, objectFormat := range []string{consumerSHA1, consumerSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()

			git, root := newConsumerCheckout(t, objectFormat)
			before := mustLayerGit(t, git, root, "count-objects", "-v")

			consumer, err := openConsumer(t.Context(), git, root, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}

			// A previous trial may have changed the private index. A snapshot
			// must still export the exact admitted tree, including modes,
			// links, binary content and the consumer's original engine pin.
			if err = consumer.stageFile(t.Context(), "100644", "README.md", "previous trial\n"); err != nil {
				t.Fatal(err)
			}

			if err = consumer.stageFile(t.Context(), "100644", consumerCanary, consumerCanaryText); err != nil {
				t.Fatal(err)
			}

			commit, err := consumer.commitSnapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}

			remote := t.TempDir()
			mustLayerGit(t, git, remote, "init", "--quiet", "--bare", "--object-format="+objectFormat)

			if err = git.push(t.Context(), consumer.stage, remote, commit, defaultBranch); err != nil {
				t.Fatal(err)
			}

			if got := mustLayerGit(t, git, remote, "rev-list", "--parents", defaultBranch); got != commit {
				t.Errorf("snapshot received history: %s", got)
			}

			wantTree := mustLayerGit(t, git, root, "rev-parse", "HEAD^{tree}")
			if got := mustLayerGit(t, git, remote, "rev-parse", defaultBranch+"^{tree}"); got != wantTree {
				t.Errorf("snapshot tree %s differs from admitted tree %s", got, wantTree)
			}

			if got := mustLayerGit(t, git, root, "count-objects", "-v"); got != before {
				t.Errorf("snapshot wrote consumer objects: before\n%s\nafter\n%s", before, got)
			}

			if got := mustLayerGit(t, git, root, "status", "--porcelain"); got != "" {
				t.Errorf("snapshot changed the consumer checkout: %s", got)
			}
		})
	}
}

func TestConsumer_TheChangeUnderReviewIsOneSignedCommitAConsumerCanLint(t *testing.T) {
	t.Parallel()
	evidence.RequireTool(t, "ssh-keygen")

	git, root := newConsumerCheckout(t, consumerSHA256)

	consumer, err := openConsumer(t.Context(), git, root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	base, err := consumer.commit(t.Context(), consumerBaseMessage)
	if err != nil {
		t.Fatal(err)
	}

	SSHKey(t, consumer.stage)

	change, err := consumer.commitChange(t.Context(), base, filepath.Join(consumer.stage, sshKeyFile))
	if err != nil {
		t.Fatal(err)
	}

	stage := func(args ...string) string { return mustLayerGit(t, git, consumer.stage, args...) }

	if parents := stage("rev-list", "--parents", "-n", "1", change); parents != change+" "+base {
		t.Errorf("the change is %q, want one commit on %s", parents, base)
	}

	if changed := stage("diff-tree", "-r", "--name-status", base, change); changed != "A\t"+consumerCanary {
		t.Errorf("the change is:\n%s\nwant the canary added", changed)
	}

	if object := stage("cat-file", "commit", change); !strings.Contains(object, "\ngpgsig-sha256 -----BEGIN SSH SIGNATURE-----\n") {
		t.Errorf("the change carries no SSH signature:\n%s", object)
	}

	// What a consumer's commit check reads: a Conventional subject, a blank
	// line, a sign-off by the author, and no line over 72 characters.
	for _, message := range []string{consumerBaseMessage, consumerChangeMessage} {
		lines := strings.Split(message, "\n")
		if len(lines) != 3 || !strings.HasPrefix(lines[0], "test: ") || lines[1] != "" || lines[2] != consumerSignOff {
			t.Errorf("message %q is not a Conventional subject and a sign-off", message)
		}

		for _, line := range lines {
			if len(line) > 72 {
				t.Errorf("message line %q is over 72 characters", line)
			}
		}
	}

	if author := stage("log", "-1", "--format=%an <%ae>", change); consumerSignOff != "Signed-off-by: "+author {
		t.Errorf("the change is by %q and signed off as %q", author, consumerSignOff)
	}
}

func TestOpenConsumer_RefusesWhatIsNotACleanCheckoutTop(t *testing.T) {
	t.Parallel()

	git, root := newConsumerCheckout(t, consumerSHA1)

	for name, path := range map[string]string{"a relative path": "consumer", "a directory inside": filepath.Join(root, "policy"), "no repository": t.TempDir()} {
		if consumer, err := openConsumer(t.Context(), git, path, t.TempDir()); err == nil {
			t.Errorf("%s: openConsumer = %+v, want a refusal", name, consumer)
		}
	}

	writeLayerFile(t, root, "README.md", "edited\n")
	writeLayerFile(t, root, "notes/new.txt", "untracked\n")

	_, err := openConsumer(t.Context(), git, root, t.TempDir())
	if !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "README.md") || !strings.Contains(err.Error(), "notes/new.txt") {
		t.Errorf("a dirty checkout: %v; want a refusal that names README.md and notes/new.txt", err)
	}
}
