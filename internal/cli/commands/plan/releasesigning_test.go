// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package plan_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	plancmd "github.com/diggsweden/forge-ci/internal/cli/commands/plan"
	domainsummary "github.com/diggsweden/forge-ci/internal/domain/summary"
	"github.com/diggsweden/forge-ci/internal/testutil/ghaenv"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// releasePlans are the plan steps of the Forgejo and GitLab releases, the
// calls that warn: Forgejo's prepare plan job writes the version matrix, and
// GitLab's "Release: plan" the release jobs. Every family has its image.
func releasePlans(declaration, dir string) map[string][]string {
	image := func(family string) string {
		return "registry.example/" + family + "@sha256:" + strings.Repeat("1", 64)
	}

	return map[string][]string{
		"forgejo": {
			"plan", "release-versions", "--file", declaration, "--matrix-output", filepath.Join(dir, "forgejo.json"),
			"--runtime-image-java", image("java"), "--runtime-image-node", image("node"), "--runtime-image-rust", image("rust"),
		},
		"gitlab": {
			"plan", "gitlab-release-pipeline", "--file", declaration, "--output", filepath.Join(dir, "gitlab.yml"),
			"--runtime-image-java", image("java"), "--runtime-image-node", image("node"), "--runtime-image-go", image("go"),
			"--runtime-image-rust", image("rust"), "--runtime-image-android", image("android"),
		},
	}
}

// A sign.method or sign.key the release does not use, and a sign.transparency
// its input decides, is a warning in the job summary, never a refusal: one
// declaration often serves several forges.
func TestReleasePlans_WarnAboutSigningTheReleaseDoesNotUse(t *testing.T) { //nolint:paralleltest // the step summary is the environment's.
	env := ghaenv.Setup(t)

	const cosignKey = "the Cosign key (RELEASE_COSIGN_PRIVATE_KEY)"

	for _, tc := range []struct {
		name, sign string
		warn       map[string][]string
	}{
		{name: "no sign block"},
		{name: "the Cosign key", sign: "sign:\n  method: kms\n  key: env://COSIGN_KEY\n"},
		{name: "gpg", sign: "sign:\n  method: gpg\n"},
		{name: "gpg files", sign: "sign:\n  method: kms\n  key: env://COSIGN_KEY\n  files-method: gpg\n"},
		{name: "sigstore", sign: "sign:\n  method: sigstore\n", warn: map[string][]string{
			"forgejo": {"sign.method sigstore is not available on the forgejo release", cosignKey},
			"gitlab":  {"sign.method sigstore is not available on the gitlab release", cosignKey},
		}},
		{name: "another key", sign: "sign:\n  method: kms\n  key: awskms:///alias/release\n", warn: map[string][]string{
			"forgejo": {`sign.key "awskms:///alias/release" is not used by the forgejo release`, cosignKey},
			"gitlab":  {`sign.key "awskms:///alias/release" is not used by the gitlab release`, cosignKey},
		}},
		{name: "transparency", sign: "sign:\n  method: kms\n  key: env://COSIGN_KEY\n  transparency: public\n", warn: map[string][]string{
			"forgejo": {"sign.transparency public is not used by the forgejo release; its transparency-log input decides"},
			"gitlab":  {"sign.transparency public is not used by the gitlab release; its transparency-log input decides"},
		}},
	} {
		dir := t.TempDir()
		declaration := filepath.Join(dir, "artifacts.yml")
		require.NoError(t, os.WriteFile(declaration, []byte("artifacts:\n  - name: app\n    project-type: maven\n"+tc.sign), 0o600))

		for forge, args := range releasePlans(declaration, dir) {
			require.NoError(t, os.WriteFile(env.SummaryPath, nil, 0o600))
			require.NoError(t, plancmd.New().Run(t.Context(), args), "%s on %s", tc.name, forge)

			summary := env.Summary()
			if len(tc.warn[forge]) == 0 {
				require.NotContains(t, summary, "Release signing", "%s on %s", tc.name, forge)
			}

			for _, want := range tc.warn[forge] {
				// The summary renders the warning as literal Markdown text.
				require.Contains(t, summary, domainsummary.LiteralText(want), "%s on %s", tc.name, forge)
			}
		}

		// The re-checks of the same declaration later in the run do not repeat
		// the warning.
		for _, verb := range []string{"release-versions", "gitlab-release-pipeline"} {
			require.NoError(t, os.WriteFile(env.SummaryPath, nil, 0o600))
			require.NoError(t, plancmd.New().Run(t.Context(), []string{
				"plan", verb, "--file", declaration, "--prepare-plan-output", filepath.Join(dir, "prepare.json"),
			}), "%s re-check of %s", verb, tc.name)
			require.NotContains(t, env.Summary(), "Release signing", "%s re-check of %s", verb, tc.name)
		}
	}
}

// Every demo declaration plans through the Forgejo and the GitLab release:
// the demos share one artifacts.yml between the forges, so a release plan that
// refused one would stop that demo's release on the forge.
func TestReleasePlans_PlanEveryDemoDeclaration(t *testing.T) { //nolint:paralleltest // the step summary is the environment's.
	ghaenv.Setup(t)

	consumers := filepath.Join(reporoot.Path(t), "test", "live", "fixtures", "consumers")

	// Each demo's declaration, which every forge's lane releases with, and the
	// overlays only a Forgejo lane releases with.
	forges := map[string][]string{}

	for pattern, served := range map[string][]string{
		filepath.Join(consumers, "*", "consumer", ".forge-ci", "artifacts.yml"):                              {"forgejo", "gitlab"},
		filepath.Join(consumers, "*", "consumer", "test", "live", "forgejo-*", ".forge-ci", "artifacts.yml"): {"forgejo"},
	} {
		matches, err := filepath.Glob(pattern)
		require.NoError(t, err)
		require.NotEmpty(t, matches, pattern)

		for _, match := range matches {
			forges[match] = served
		}
	}

	for declaration, served := range forges {
		demo, err := filepath.Rel(consumers, declaration)
		require.NoError(t, err)

		dir := t.TempDir()

		// GitLab's job list also refuses publishing it has no registry for, a
		// rule of its own, so GitLab plans the release as every release job
		// does, which is also where a refused declaration would stop it.
		plans := map[string][]string{
			"forgejo": releasePlans(declaration, dir)["forgejo"],
			"gitlab":  {"plan", "gitlab-release-pipeline", "--file", declaration, "--prepare-plan-output", filepath.Join(dir, "gitlab.json")},
		}

		for _, forge := range served {
			args := plans[forge]
			err := plancmd.New().Run(t.Context(), args)
			require.NoError(t, err, "%s on %s: %s", demo, forge, strings.Join(args, " "))
		}
	}
}
