// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
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

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// preparedImages is a prepared.env with every image a Forgejo case can name.
var preparedImages = map[string]string{
	"JOB_IMAGE_GO":                    "registry.invalid/go@sha256:" + strings.Repeat("1", 64),
	"JOB_IMAGE_JAVA":                  "registry.invalid/java@sha256:" + strings.Repeat("2", 64),
	"JOB_IMAGE_JAVASCRIPT_TYPESCRIPT": "registry.invalid/js@sha256:" + strings.Repeat("3", 64),
	"JOB_IMAGE_RUST":                  "registry.invalid/rust@sha256:" + strings.Repeat("4", 64),
	KanikoJobImageArchiveEnv:          "/c/kaniko.tar",
}

// A Forgejo case's settings are its flavor's prepared nanolinter image, its
// builder and what the case names, each left out when empty. A build with
// kaniko takes no kaniko job image here, the run pushing it for its own
// account, but needs its archive. An image that was not prepared is refused
// by its key.
func TestForgejoSettingsOfACase(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		c    PreparedCase
		want []string
	}{
		"buildah snapshot": {PreparedCase{Event: TrialSnapshot, JobImage: flavorRust, Builder: ImageBuilderBuildah}, []string{
			"CI_JOB_IMAGE=" + preparedImages["JOB_IMAGE_RUST"], "CI_IMAGE_BUILDER=buildah",
		}},
		"snapshot of a variant declaration": {PreparedCase{Event: TrialSnapshot, JobImage: flavorJava, Builder: ImageBuilderBuildah, SnapshotDeclaration: mavenServiceBuildSBOMDeclaration}, []string{
			"CI_JOB_IMAGE=" + preparedImages["JOB_IMAGE_JAVA"], "CI_IMAGE_BUILDER=buildah", "snapshot-declaration=" + mavenServiceBuildSBOMDeclaration,
		}},
		"kaniko lifecycle": {PreparedCase{Event: TrialLifecycle, JobImage: flavorJava, Builder: ImageBuilderKaniko, Platforms: nativePlatform}, []string{
			"CI_JOB_IMAGE=" + preparedImages["JOB_IMAGE_JAVA"], "CI_IMAGE_BUILDER=kaniko", "RELEASE_PLATFORMS=linux/amd64",
		}},
		"gate with a front end": {PreparedCase{Event: TrialPullRequest, JobImage: flavorJava, Builder: ImageBuilderBuildah, NodeJobImage: flavorJS}, []string{
			"CI_JOB_IMAGE=" + preparedImages["JOB_IMAGE_JAVA"], "CI_IMAGE_BUILDER=buildah",
			"CI_JOB_IMAGE_NODE=" + preparedImages["JOB_IMAGE_JAVASCRIPT_TYPESCRIPT"],
		}},
		"release with every setting": {PreparedCase{
			Event: TrialRelease, JobImage: flavorJS, Builder: ImageBuilderBuildah,
			Version: "0.2.0", NextVersion: "0.2.1", ReleaseImagesConfig: ".forge-ci/images.yml", Platforms: nativePlatform,
			Fixtures: ForgejoPackagesFixtures,
		}, []string{
			"CI_JOB_IMAGE=" + preparedImages["JOB_IMAGE_JAVASCRIPT_TYPESCRIPT"], "CI_IMAGE_BUILDER=buildah",
			"version=0.2.0", "next-version=0.2.1", "release-images-config=.forge-ci/images.yml", "RELEASE_PLATFORMS=linux/amd64",
			"fixtures=" + ForgejoPackagesFixtures,
		}},
	} {
		if got, err := test.c.Settings(preparedImages); err != nil || !slices.Equal(got, test.want) {
			t.Errorf("%s: settings %q, %v; want %q", name, got, err, test.want)
		}
	}

	kaniko := PreparedCase{Event: TrialRelease, JobImage: flavorGo, Builder: ImageBuilderKaniko}

	for _, missing := range []string{"JOB_IMAGE_GO", KanikoJobImageArchiveEnv} {
		prepared := maps.Clone(preparedImages)
		delete(prepared, missing)

		if _, err := kaniko.Settings(prepared); !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "has no "+missing+";") {
			t.Errorf("without %s: %v", missing, err)
		}
	}

	// A pull request builds no image, so it needs no kaniko job image.
	prepared := maps.Clone(preparedImages)
	delete(prepared, KanikoJobImageArchiveEnv)

	if _, err := (PreparedCase{Event: TrialPullRequest, JobImage: flavorGo, Builder: ImageBuilderKaniko}).Settings(prepared); err != nil {
		t.Errorf("a kaniko pull request without the archive: %v", err)
	}
}

// Every case's settings are ones its trial takes, so a case the trial would
// refuse costs no run to learn: a job image by digest, kaniko with its job
// image, and on GitLab a release's and a lifecycle's version and job user.
func TestPreparedCasesHaveSettingsTheirTrialTakes(t *testing.T) {
	t.Parallel()

	for _, c := range ForgejoPreparedCases {
		settings, err := c.Settings(preparedImages)
		if c.BuildsWithKaniko() {
			// What the run pushes from the archive.
			settings = append(settings, "CI_IMAGE_BUILDER_JOB_IMAGE=registry.invalid/kaniko@sha256:"+strings.Repeat("5", 64))
		}

		if err == nil {
			_, err = ParseTrialSettings(c.Event, settings)
		}

		if err != nil {
			t.Errorf("forgejo/%s: %v", c.Name(), err)
		}
	}

	for _, gitLabCase := range GitLabPreparedCases {
		var err error

		switch settings := strings.Join(gitLabCase.GitLabSettings(), "\n"); gitLabCase.Event {
		case TrialRelease:
			var parsed GitLabReleaseSettings
			if parsed, err = ParseGitLabReleaseSettings(settings); parsed.Fixtures != gitLabCase.Fixtures {
				t.Errorf("gitlab/%s: the trial stages the fixtures %q, the case names %q", gitLabCase.Name(), parsed.Fixtures, gitLabCase.Fixtures)
			}
		case TrialLifecycle:
			_, err = ParseGitLabLifecycleSettings(settings)
		case TrialSnapshot:
			var declaration string
			if declaration, err = GitLabSnapshotDeclaration(settings); declaration != gitLabCase.SnapshotDeclaration {
				t.Errorf("gitlab/%s: the trial reads the declaration %q, the case names %q", gitLabCase.Name(), declaration, gitLabCase.SnapshotDeclaration)
			}
		default:
			if settings != "" {
				t.Errorf("gitlab/%s takes its images alone, and has the settings %q", gitLabCase.Name(), settings)
			}
		}

		if err != nil {
			t.Errorf("gitlab/%s: %v", gitLabCase.Name(), err)
		}
	}
}

// gitLabPrepared is a prepared.env with every archive a GitLab case can name.
var gitLabPrepared = map[string]string{
	"FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE_GO": "/c/go.tar", "FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE_JAVA": "/c/java.tar",
	"FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE_NODE": "/c/node.tar", "FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE_RUST": "/c/rust.tar",
	"FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE_ANDROID": "/c/android.tar", "FORGE_CI_LIVE_SIGNER_IMAGE_ARCHIVE": "/c/signer.tar",
	"FORGE_CI_LIVE_BUILDAH_JOB_IMAGE_ARCHIVE": "/c/buildah.tar", "LINT_IMAGE_ARCHIVE_GO": "/c/lint-go.tar",
	"LINT_IMAGE_ARCHIVE_JAVA": "/c/lint-java.tar", "LINT_IMAGE_ARCHIVE_RUST": "/c/lint-rust.tar",
	"LINT_IMAGE_ARCHIVE_JAVASCRIPT_TYPESCRIPT": "/c/lint-js.tar",
}

// A lifecycle without its signer or lint archive is refused by name, and no
// archive is taken from anywhere but the prepared directory.
func TestGitLabEnvironmentNamesTheMissingArchive(t *testing.T) {
	t.Parallel()

	lifecycle := gitLabCaseOf(t, TrialLifecycle)

	for _, missing := range []string{"FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE_JAVA", "FORGE_CI_LIVE_SIGNER_IMAGE_ARCHIVE", "LINT_IMAGE_ARCHIVE_JAVA"} {
		prepared := maps.Clone(gitLabPrepared)
		delete(prepared, missing)

		_, err := lifecycle.GitLabEnvironment(prepared)
		if !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "has no "+missing+";") {
			t.Errorf("without %s: %v", missing, err)
		}
	}

	snapshot, err := gitLabCaseOf(t, TrialSnapshot).GitLabEnvironment(gitLabPrepared)
	if err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{gitLabBuildArchiveEnv, gitLabSignerArchiveEnv, gitLabLintArchiveEnv, KanikoJobImageArchiveEnv} {
		if value, cleared := snapshot[key]; !cleared || value != "" {
			t.Errorf("a snapshot's %s is %q, cleared=%t", key, value, cleared)
		}
	}
}

// A snapshot takes the runtime archive of its flavor as its job image, and
// builds in the saved job image of its own builder: without that archive the
// case is refused by name, whatever the other builder's is.
func TestGitLabEnvironmentOfASnapshot(t *testing.T) {
	t.Parallel()

	both := maps.Clone(gitLabPrepared)
	both[KanikoJobImageArchiveEnv] = "/c/kaniko.tar"

	for _, builder := range []string{ImageBuilderBuildah, ImageBuilderKaniko} {
		snapshot := PreparedCase{Demo: "hello-cargo-workspace", Event: TrialSnapshot, JobImage: flavorRust, Builder: builder}
		archive := GitLabSnapshotBuilderArchiveEnv(builder)

		got, err := snapshot.GitLabEnvironment(both)
		if err != nil || got[gitLabJobArchiveEnv] != "/c/rust.tar" || got[archive] != both[archive] {
			t.Errorf("a %s snapshot: %v, %v", builder, got, err)
		}

		prepared := maps.Clone(both)
		delete(prepared, archive)

		if _, err := snapshot.GitLabEnvironment(prepared); !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "has no "+archive+";") {
			t.Errorf("a %s snapshot without %s: %v", builder, archive, err)
		}
	}
}

// gitLabCaseOf is the first GitLab case of the event.
func gitLabCaseOf(t *testing.T, event string) PreparedCase {
	t.Helper()

	index := slices.IndexFunc(GitLabPreparedCases, func(c PreparedCase) bool { return c.Event == event })
	if index < 0 {
		t.Fatalf("no GitLab case of %s", event)
	}

	return GitLabPreparedCases[index]
}

// A merge request takes its job archive as the consumer image, which leaves
// the release's job archive empty, and its flavor's lint archive; a release
// builds in its job image and signs. An Android gate runs in the android
// runtime image and lints in the java flavor's.
func TestGitLabEnvironmentOfAMergeRequestAndARelease(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		c    PreparedCase
		want map[string]string
	}{
		"merge request": {PreparedCase{Demo: "hello-maven", Event: TrialPullRequest, JobImage: flavorJava}, map[string]string{
			gitLabConsumerArchiveEnv: "/c/java.tar", gitLabLintArchiveEnv: "/c/lint-java.tar",
			gitLabJobArchiveEnv: "", gitLabBuildArchiveEnv: "", gitLabSignerArchiveEnv: "",
			// Every runtime archive, for a gate's further job images, and the
			// builders' job images.
			"FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE_NODE": "/c/node.tar", "FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE_JAVA": "/c/java.tar",
			"FORGE_CI_LIVE_BUILDAH_JOB_IMAGE_ARCHIVE": "/c/buildah.tar",
		}},
		"android merge request": {PreparedCase{Demo: "hello-android-library", Event: TrialPullRequest, JobImage: flavorJava, Runtime: "android"}, map[string]string{
			gitLabConsumerArchiveEnv: "/c/android.tar", gitLabLintArchiveEnv: "/c/lint-java.tar", gitLabJobArchiveEnv: "",
		}},
		"release": {PreparedCase{Demo: "hello-maven", Event: TrialRelease, JobImage: flavorJava, Version: "0.2.0"}, map[string]string{
			gitLabJobArchiveEnv: "/c/java.tar", gitLabBuildArchiveEnv: "/c/java.tar", gitLabSignerArchiveEnv: "/c/signer.tar", gitLabLintArchiveEnv: "/c/lint-java.tar",
		}},
		"lifecycle": {PreparedCase{Demo: "hello-npm-service", Event: TrialLifecycle, JobImage: flavorJS, Version: "0.2.0"}, map[string]string{
			gitLabJobArchiveEnv: "/c/node.tar", gitLabBuildArchiveEnv: "/c/node.tar", gitLabSignerArchiveEnv: "/c/signer.tar", gitLabLintArchiveEnv: "/c/lint-js.tar",
		}},
	} {
		got, err := test.c.GitLabEnvironment(gitLabPrepared)
		if err != nil {
			t.Fatal(err)
		}

		for key, want := range test.want {
			if value, set := got[key]; !set || value != want {
				t.Errorf("%s: %s is %q, set=%t; want %q", name, key, value, set, want)
			}
		}

		if _, set := got[gitLabConsumerArchiveEnv]; set && test.c.Event != TrialPullRequest {
			t.Errorf("%s takes a consumer image archive", name)
		}
	}

	release := PreparedCase{Event: TrialRelease, JobImage: flavorJava, Version: "0.2.0"}
	if got := release.GitLabSettings(); !slices.Equal(got, []string{"version=0.2.0", "job-user=65532:65532"}) {
		t.Errorf("a release's settings are %q", got)
	}

	if got := (PreparedCase{Event: TrialLifecycle, JobImage: flavorJava, Version: "0.2.0"}).GitLabSettings(); !slices.Equal(got, []string{"version=0.2.0", "job-user=65532:65532"}) {
		t.Errorf("a lifecycle's settings are %q", got)
	}

	if got := (PreparedCase{Event: TrialPullRequest, JobImage: flavorJava}).GitLabSettings(); got != nil {
		t.Errorf("a merge request's settings are %q", got)
	}

	for _, missing := range []string{"FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE_JAVA", "LINT_IMAGE_ARCHIVE_JAVA"} {
		prepared := maps.Clone(gitLabPrepared)
		delete(prepared, missing)

		_, err := PreparedCase{Event: TrialPullRequest, JobImage: flavorJava}.GitLabEnvironment(prepared)
		if !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "has no "+missing+";") {
			t.Errorf("a merge request without %s: %v", missing, err)
		}
	}
}

// A private lane's cases cannot be run public, and on GitLab they keep the
// reproducible-build check the lane always ran with.
func TestPrivateLaneFixesItsVisibilityAndReproducibleBuilds(t *testing.T) {
	t.Parallel()

	private := PreparedCase{Lane: LanePrivate}

	if got := private.LaneEnvironment(forgeForgejo); !maps.Equal(got, map[string]string{"FORGE_CI_LIVE_CONSUMER_VISIBILITY": "private"}) {
		t.Errorf("a private Forgejo case fixes %v", got)
	}

	if got := private.LaneEnvironment(forgeGitLab); !maps.Equal(got, map[string]string{
		"FORGE_CI_LIVE_CONSUMER_VISIBILITY": "private", "FORGE_CI_LIVE_ADMIN_REPRODUCIBLE_BUILDS": "true",
	}) {
		t.Errorf("a private GitLab case fixes %v", got)
	}

	if got := (PreparedCase{Lane: LaneLifecycle}).LaneEnvironment(forgeGitLab); got != nil {
		t.Errorf("a lifecycle case fixes %v", got)
	}

	// The check is turned on only where a component takes its input: turned
	// on where none does, the run's record would claim a check that never ran.
	release, err := os.ReadFile(filepath.Join(reporoot.Path(t), "templates", "release.yml"))
	if err != nil || !strings.Contains(string(release), "enforce-reproducible-builds:") {
		t.Errorf("GitLab's release component no longer takes enforce-reproducible-builds: %v", err)
	}
}

// Every GitLab case finds, in a directory prepared for all of them, the
// archive of each further runtime image its demo runs in; the monorepo's
// snapshot without its Node producer's archive is refused by name rather
// than left to report itself not covered.
func TestGitLabCaseRequiresItsFurtherArchives(t *testing.T) {
	t.Parallel()

	repo := reporoot.Path(t)

	for _, c := range GitLabPreparedCases {
		if err := c.RequireGitLabFurtherArchives(demoConsumerDir(repo, c.Demo), gitLabPrepared); err != nil {
			t.Errorf("%s: %v", c.Name(), err)
		}
	}

	const missing = "FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE_NODE"

	prepared := maps.Clone(gitLabPrepared)
	delete(prepared, missing)

	monorepo := PreparedCase{Lane: LaneSnapshot, Demo: "hello-monorepo", Event: TrialSnapshot, JobImage: flavorJava, Builder: ImageBuilderBuildah}
	if err := monorepo.RequireGitLabFurtherArchives(demoConsumerDir(repo, monorepo.Demo), prepared); !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "has no "+missing+";") {
		t.Errorf("the monorepo snapshot without %s: %v", missing, err)
	}
}

// A Forgejo trial takes the signer and the builders' saved job images from
// the prepared directory alone: one that was not prepared is set empty, so
// a value left in the caller's environment is never taken for it.
func TestPreparedForgejoEnvironmentClearsWhatWasNotPrepared(t *testing.T) {
	t.Parallel()

	buildah := GitLabSnapshotBuilderArchiveEnv(ImageBuilderBuildah)
	got := PreparedForgejoEnvironment(map[string]string{signerImageEnv: "signer@sha256:1", buildah: "/c/buildah.tar", "LINT_IMAGE_ARCHIVE_GO": "/c/lint.tar"})

	if want := map[string]string{signerImageEnv: "signer@sha256:1", buildah: "/c/buildah.tar", KanikoJobImageArchiveEnv: ""}; !maps.Equal(got, want) {
		t.Errorf("environment %v, want %v", got, want)
	}
}

// A case's name is its subtest and its evidence key, so no two cases of a
// forge share one.
func TestPreparedCaseNamesAreUnique(t *testing.T) {
	t.Parallel()

	for _, forge := range []string{forgeForgejo, forgeGitLab} {
		seen := map[string]bool{}

		for _, c := range PreparedCases(forge) {
			if seen[c.Name()] {
				t.Errorf("%s has two cases named %s", forge, c.Name())
			}

			seen[c.Name()] = true
		}
	}
}

// A scenario of a lane takes its images and its lane's settings from
// the prepared directory, a consumer case from there, or, the interruption
// and the native summary probe alone, nothing; so none is left to the
// caller's environment. A missing
// image is refused, naming its key.
func TestPreparedScenarioEnvironment_GivesALanesScenarioItsImagesAndSettings(t *testing.T) {
	t.Parallel()

	prepared := map[string]string{
		gitLabJobArchiveEnv: "/c/go.tar", signerImageEnv: "registry.invalid/signer@sha256:" + strings.Repeat("6", 64),
		gitLabSignerArchiveEnv: "/c/signer.tar", KanikoJobImageArchiveEnv: "/c/kaniko.tar",
		GitLabSnapshotBuilderArchiveEnv(ImageBuilderBuildah): "/c/buildah.tar",
	}

	for _, lane := range PreparedLanes {
		for _, scenario := range lane.Scenarios {
			environment, err := PreparedScenarioEnvironment(scenario, prepared)
			if err != nil {
				t.Fatal(err)
			}

			_, takesCase := PreparedScenarioCase(lane.Forge, scenario)

			var want map[string]string

			switch {
			case lane.Name == laneK3sPrivateGitLab:
				want = map[string]string{
					consumerVisibilityEnv: visibilityPrivate, "FORGE_CI_LIVE_ADMIN_REPRODUCIBLE_BUILDS": "true", GitLabImageBuilderEnv: ImageBuilderKaniko,
					gitLabJobArchiveEnv: "/c/go.tar", signerImageEnv: prepared[signerImageEnv],
					gitLabSignerArchiveEnv: "/c/signer.tar", KanikoJobImageArchiveEnv: "/c/kaniko.tar",
				}
			case lane.Name == laneBuildCapabilityGitLab:
				want = map[string]string{consumerVisibilityEnv: visibilityPrivate, GitLabImageBuilderEnv: ImageBuilderBuildah, gitLabBuildArchiveEnv: "/c/buildah.tar"}
			case !takesCase && lane.Name != laneInterruptionManualGitLab && scenario != forgejoNativeSummaryTest:
				t.Errorf("%s: %s takes neither a lane's environment nor a case from the prepared directory", lane.Name, scenario)
			}

			if !maps.Equal(environment, want) {
				t.Errorf("%s: %s: environment %v, want %v", lane.Name, scenario, environment, want)
			}
		}
	}

	for scenario, key := range map[string]string{
		"TestGitLabRelease_MinimalSignedArtifacts": gitLabSignerArchiveEnv,
		"TestGitLabRelease_BuildImageCapability":   GitLabSnapshotBuilderArchiveEnv(ImageBuilderBuildah),
	} {
		lacking := maps.Clone(prepared)
		delete(lacking, key)

		if _, err := PreparedScenarioEnvironment(scenario, lacking); !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), key) {
			t.Errorf("%s without %s: %v", scenario, key, err)
		}
	}
}
