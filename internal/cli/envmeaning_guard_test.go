// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package cli_test

import (
	"maps"
	"slices"
	"strings"
	"testing"

	urfavecli "github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/cli"
	"github.com/diggsweden/forge-ci/internal/runcontext"
)

// sharedEnvSpelling records an environment variable that more than one flag
// spelling reads. Canonical is the flag name most of its readers use; Sites
// lists every other reader as "<command path> --<flag>", each one a decision
// that it reads the SAME value under a different flag name.
type sharedEnvSpelling struct {
	Canonical string
	Sites     []string
	Why       string
}

// TestEnvVarHasOneMeaningAcrossCommands walks the command tree and fails when
// an environment variable feeds flags of different spellings that nobody has
// recorded as the same setting.
//
// A job's env is ambient: every forge-ci call in the job (or step) sees it. A
// name that is the project's artifact name to one command and the transfer
// name to another hands one meaning to the other the moment both run in the
// same job. That is how `artifact download --pattern` failed with "use --name
// or --pattern" in a release job that set ARTIFACT_NAME for its project.
//
// The flag name is the meaning a reader can see, so it is what is compared. A
// different spelling is either the same setting under drifted naming, which
// gets a row in sharedEnvSpellings saying so, or a different setting, which
// must not read that name.
//
// Run-context names are skipped: runcontext owns them, and the concept guards
// (TestOneFlagNamePerRunContextConcept, TestNoFlagCarriesAPartialRunContextChain)
// already hold every binding of one to the same chain.
func TestEnvVarHasOneMeaningAcrossCommands(t *testing.T) {
	t.Parallel()

	owned := map[string]bool{}

	for _, v := range runcontext.All() {
		for _, key := range v.Keys() {
			owned[key] = true
		}
	}

	readers := map[string]map[string][]string{} // env key -> flag name -> sites

	var walk func(path string, cmd *urfavecli.Command)

	walk = func(path string, cmd *urfavecli.Command) {
		for _, flag := range cmd.Flags {
			for _, key := range envKeysOf(t, flag) {
				if owned[key] {
					continue
				}

				if readers[key] == nil {
					readers[key] = map[string][]string{}
				}

				name := flagName(flag)
				readers[key][name] = append(readers[key][name], path+" --"+name)
			}
		}

		for _, sub := range cmd.Commands {
			walk(path+" "+sub.Name, sub)
		}
	}

	walk("forge-ci", cli.New(cli.BuildInfo{Version: "dev"}))

	if len(readers) == 0 {
		t.Fatal("no env-backed flags found; the walk, not the command tree, is what was measured")
	}

	if !slices.Contains(readers["ARTIFACT_NAME"]["artifact-name"], "forge-ci release resolve metadata --artifact-name") {
		t.Fatal("the walk never reached release resolve metadata --artifact-name; it is not auditing the tree")
	}

	shared := sharedEnvSpellings()

	for _, key := range slices.Sorted(maps.Keys(readers)) {
		byName := readers[key]
		row, recorded := shared[key]

		if !recorded {
			if len(byName) > 1 {
				t.Errorf("$%s is read under %d flag names:%s\n"+
					"    One environment name, one setting. If these are different settings, give the odd one "+
					"out its own name (or no env binding). If they are the same setting, add a row to "+
					"sharedEnvSpellings saying why.", key, len(byName), listReaders(byName))
			}

			continue
		}

		var got []string

		for name, sites := range byName {
			if name != row.Canonical {
				got = append(got, sites...)
			}
		}

		slices.Sort(got)

		want := slices.Sorted(slices.Values(row.Sites))
		if len(byName[row.Canonical]) == 0 || !slices.Equal(got, want) {
			t.Errorf("$%s: recorded canonical --%s plus %v, but the tree reads it as:%s\n"+
				"    A new reader under another flag name is a new meaning until shown otherwise; a recorded "+
				"site that is gone leaves the row describing nothing. Update the row, with its reason.",
				key, row.Canonical, want, listReaders(byName))
		}
	}

	for key, row := range shared {
		if row.Why == "" {
			t.Errorf("sharedEnvSpellings row $%s gives no reason; a row is a claim that its readers mean one thing", key)
		}

		if _, ok := readers[key]; !ok {
			t.Errorf("sharedEnvSpellings has a row for $%s, which no flag reads any more; drop it", key)
		}
	}
}

func listReaders(byName map[string][]string) string {
	var b strings.Builder

	for _, name := range slices.Sorted(maps.Keys(byName)) {
		b.WriteString("\n      --" + name + ": " + strings.Join(byName[name], ", "))
	}

	return b.String()
}

// sharedEnvSpellings is the reasoned list of environment names read under
// more than one flag spelling. Each row is the SAME setting; renaming a flag to
// the canonical spelling removes its site from the row.
//
// ARTIFACT_NAME is the case this guard exists for. It is the PROJECT's
// artifact name; `artifact upload|download --name`, the run-artifact transfer
// name, used to read it too and no longer does. Do not add a transfer site.
func sharedEnvSpellings() map[string]sharedEnvSpelling {
	const imageRef = "the image reference the step works on; push falls back to it after $DESTINATION_REF"

	return map[string]sharedEnvSpelling{
		"ARTIFACTS_CONFIG": {Canonical: "file", Why: "path of the artifacts config file",
			Sites: []string{"forge-ci config validate --artifacts-file"}},
		"ARTIFACT_NAME": {Canonical: "artifact-name", Why: "the project's artifact name, never a run-artifact transfer name",
			Sites: []string{"forge-ci sbom build --name", "forge-ci build gradle-android run --name-override"}},
		"BINARY_NAME": {Canonical: "binary-name", Why: "the built binary's name; sbom build falls back to it after $ARTIFACT_NAME",
			Sites: []string{"forge-ci sbom build --name"}},
		"BRANCH": {Canonical: "branch", Why: "the release branch; prerequisites reads $TARGET_BRANCH first",
			Sites: []string{"forge-ci validate prerequisites --target-branch"}},
		"CHANGELOG_FILE": {Canonical: "changelog-file", Why: "path of the changelog file",
			Sites: []string{"forge-ci version commit-changelog-release --changelog", "forge-ci version render-changelog --changelog-path"}},
		"CONFIG_PLAN_JSON": {Canonical: "config-plan-json", Why: "the resolved config plan document",
			Sites: []string{"forge-ci sbom assemble --plan"}},
		"CONTAINERFILE": {Canonical: "containerfile", Why: "path of the Containerfile",
			Sites: []string{"forge-ci container containerfile-arg-default --file", "forge-ci container validate containerfile --path"}},
		"CONTAINER_FILE": {Canonical: "containerfile", Why: "path of the Containerfile",
			Sites: []string{"forge-ci container build --file"}},
		"DIST_DIR": {Canonical: "dist-dir", Why: "the release dist directory",
			Sites: []string{"forge-ci release assemble-dist --path", "forge-ci release prepare-dist --path"}},
		"GIT_COMMIT_GPGSIGN": {Canonical: "git-commit-gpgsign", Why: "git's commit.gpgsign, which covers SSH signing too",
			Sites: []string{"forge-ci release ssh setup --git-commit-sign"}},
		"HAS_RELEASE_GPG_PRIVATE_KEY": {Canonical: "has-maven-central-signing-key", Why: "whether the release GPG key secret is present",
			Sites: []string{"forge-ci report status prerequisites --has-release-gpg-private-key"}},
		"IMAGE_NAME": {Canonical: "image-name", Why: "the image name without tag or digest",
			Sites: []string{"forge-ci container build-push-oci-image --image"}},
		"IMAGE_REF": {Canonical: "image-ref", Why: imageRef, Sites: []string{
			"forge-ci container image push --destination", "forge-ci container manifest push --destination",
			"forge-ci container image-labels-json --ref", "forge-ci container manifest digest --ref",
			"forge-ci container ref canonical --ref", "forge-ci container ref name --ref",
			"forge-ci container ref platform --ref", "forge-ci container release-images validate --ref",
			"forge-ci container image-evidence --registry-ref",
		}},
		"JOB_STATUS": {Canonical: "job-status", Why: "the runner's job status",
			Sites: []string{"forge-ci report job-result --status"}},
		"MAVEN_CLI_OPTS": {Canonical: "cli-opts", Why: "extra Maven command-line options",
			Sites: []string{"forge-ci version bump --maven-cli-opts", "forge-ci version bump-plan --maven-cli-opts"}},
		"NPM_REGISTRY": {Canonical: "registry", Why: "the npm registry URL",
			Sites: []string{"forge-ci plan snapshot-release --npm-registry"}},
		"PLATFORMS": {Canonical: "platforms", Why: "the os/arch platform set; image-evidence reads $IMAGE_EVIDENCE_PLATFORMS first",
			Sites: []string{"forge-ci container image-evidence --platform"}},
		"RELEASE_ASSEMBLY": {Canonical: "assembly", Why: "the assembly document: assemble writes it, the rest read it",
			Sites: []string{"forge-ci release assemble --output"}},
		"RELEASE_DRAFT": {Canonical: "draft", Why: "whether the release is created as a draft",
			Sites: []string{"forge-ci plan release --release-draft"}},
		"RELEASE_IMAGES_PATH": {Canonical: "ledger", Why: "path of the release-images ledger",
			Sites: []string{"forge-ci release assemble-dist --release-images-path"}},
		"RELEASE_REQUEST_SHA": {Canonical: "expected-commit", Why: "the release-request commit; the tag check expects it as the parent",
			Sites: []string{"forge-ci release validate-tag --expected-parent"}},
		"RELEASE_SHA": {Canonical: "release-sha", Why: "the release commit, which the provenance check expects",
			Sites: []string{"forge-ci container release-images validate --expected-commit"}},
		"SBOMS": {Canonical: "sboms", Why: "the configured SBOM layer list",
			Sites: []string{"forge-ci config expand-sboms --value"}},
		"SIGN_ARTIFACTS": {Canonical: "sign-artifacts", Why: "whether release artifacts are signed",
			Sites: []string{"forge-ci release sbom-zip --sign"}},
	}
}
