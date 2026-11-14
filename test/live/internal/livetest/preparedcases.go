// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/enforcement"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// PreparedCase is one consumer trial of a shipped demo that a run takes from
// the prepared directory (PreparedEnv): the demo under <prepared>/demos, the
// trial's event, and the settings that vary between cases.
type PreparedCase struct {
	// Lane groups the cases one purpose runs together; it opens the case's
	// name, so `-run` selects a lane.
	Lane, Demo, Event string
	// JobImage is the flavor of the nanolinter image the demo's jobs run in,
	// CI_JOB_IMAGE, as prepared with job-image=<flavor>:<image>.
	JobImage string
	// Builder is the consumer's CI_IMAGE_BUILDER; kaniko builds in the
	// prepared kaniko job image.
	Builder string
	// Version, NextVersion and ReleaseImagesConfig are the trial's settings of
	// those names, each left out when empty.
	Version, NextVersion, ReleaseImagesConfig string
	// Platforms is RELEASE_PLATFORMS, left out when empty.
	Platforms string
	// Runtime is the runtime image target a GitLab case's jobs run in where
	// it is not its flavor's own (gitLabRuntimeTarget): the Android demos'.
	Runtime string
	// NodeJobImage is the flavor of the nanolinter image a Forgejo gate
	// checks a front end in beside its job image, as CI_JOB_IMAGE_NODE, and
	// a Forgejo snapshot builds the producers of the node family in
	// (ServiceSnapshotJobImageVariables): hello-monorepo's.
	NodeJobImage string
	// Option is the release option the case releases with and is judged by
	// (OptionDraft, OptionPrerelease, OptionRetention), the last element of
	// its name. The trial gives the release the option: on Forgejo as the
	// repository variable the demo's release workflow reads
	// (ForgejoReleaseOptionVariables), on GitLab as an input of the release
	// policy (GitLabReleaseOptionInputs).
	Option string
	// Fixtures is the trial's setting of that name, left out when empty: the
	// directory of the demo whose files the release is staged with.
	Fixtures string
	// SnapshotDeclaration is the snapshot trial's setting of that name, left
	// out when empty: a variant of the demo's declaration the snapshot is
	// taken from in place of ServiceSnapshotSource's.
	SnapshotDeclaration string
	// Set is the smallest run set the case is in, SetFull for a case of the
	// full set alone. Every case names one: a case without one would leave
	// the medium set unseen.
	Set string
	// Half is the one visibility the case runs with in a set below the full
	// one; empty, both. A private lane's cases run private in every set.
	Half string
}

// The run sets, each within the next: the small one for the loop after a
// change, the medium one for a forge's round and before a merge to main, and
// the full one, every case with both visibilities, before a release and
// before a plan closes. A case names the smallest set it is in (Set) by
// hand; nothing computes or rotates a set. Each set covers the same demos'
// events on Forgejo, GitLab and hosted GitHub (GitHubCells), but for the
// named exceptions a test keeps: the small set a pull request of each
// ecosystem, one release that bumps the version and one development build;
// the medium set every demo's events; the full set adds the second
// visibility, the second builder and a service demo's events alone. On the
// lab the small set builds and releases an image with kaniko, the private
// lane's hello-maven-service release, and builds one with Buildah, the
// development build. The builder, the visibility, the release options and
// the variants are each forge's own.
const (
	SetSmall  = "small"
	SetMedium = "medium"
	SetFull   = "full"
)

// liveSetEnv names the run set of a prepared run; unset, the full one.
const liveSetEnv = "FORGE_CI_LIVE_SET"

// ParseLiveSet is the run set that value names; none is the full one.
func ParseLiveSet(value string) (string, error) {
	switch value {
	case "", SetFull:
		return SetFull, nil
	case SetSmall, SetMedium:
		return value, nil
	default:
		return "", fmt.Errorf("the run set is %s, %s or %s, got %q: %w", SetSmall, SetMedium, SetFull, value, errs.ErrUsage)
	}
}

// InSet reports whether the case runs in set.
func (c PreparedCase) InSet(set string) bool {
	return set == SetFull || c.Set == SetSmall || (set == SetMedium && c.Set == SetMedium)
}

// SetHalf is the one visibility the case runs with in set, or none for both.
func (c PreparedCase) SetHalf(set string) string {
	if set == SetFull {
		return ""
	}

	return c.Half
}

// liveAfterEnv names the run set a delta run comes after (ParseLiveAfter);
// unset, the run is of its whole set.
const liveAfterEnv = "FORGE_CI_LIVE_AFTER"

// ParseLiveAfter is the run set that value names for a delta run of set,
// one below set, or none for a run of the whole set. A delta run runs what
// set runs and after does not, so the runs of small, medium after small and
// full after medium on one commit are one run of the full set.
func ParseLiveAfter(set, value string) (string, error) {
	if value == "" {
		return "", nil
	}

	sets := []string{SetSmall, SetMedium, SetFull}
	if at := slices.Index(sets, value); at < 0 || at >= slices.Index(sets, set) {
		return "", fmt.Errorf("after names a run set below the %s set, got %q: %w", set, value, errs.ErrUsage)
	}

	return value, nil
}

// Visibilities are the visibilities the case runs with in set, less those
// it ran with in the run set after: none outside set, and of a case that
// after ran with one visibility and set runs with both, the other one.
func (c PreparedCase) Visibilities(set, after string) []string {
	return setDelta(func(set string) []string { return visibilitiesIn(c, set) }, set, after)
}

// visibilitiesIn are the visibilities c runs with in set, none outside it or
// for no set.
func visibilitiesIn(c PreparedCase, set string) []string {
	switch {
	case set == "" || !c.InSet(set):
		return nil
	case c.SetHalf(set) != "":
		return []string{c.SetHalf(set)}
	default:
		return []string{visibilityPublic, visibilityPrivate}
	}
}

// setDelta are the visibilities in set less those in after, as in gives
// them for a set.
func setDelta(in func(set string) []string, set, after string) []string {
	ran := in(after)

	return slices.DeleteFunc(in(set), func(visibility string) bool { return slices.Contains(ran, visibility) })
}

// The lanes of the tables; PreparedLanes names each as the matrices do.
const (
	// LaneLifecycle: each demo with a development route through pull request,
	// merge, service snapshot and signed release, with either builder.
	LaneLifecycle = "lifecycle"
	// LaneSnapshot: the snapshot of each multi-image demo, of the demos
	// without a container, whose snapshots keep their files, of the Go
	// service, the small set's development build, and of one service from
	// a variant of its declaration (SnapshotDeclaration).
	LaneSnapshot = "snapshot"
	// LaneReleaseVariants: two releases in a row (NextVersion), release
	// images read back (ReleaseImagesConfig), for hello-npm-service the
	// package of the same release read from the owner's registry, and a
	// release with each release option (Option), with Buildah; and one pull
	// request with Buildah. On GitLab, hello-npm-service's release of files,
	// a package and an image together.
	LaneReleaseVariants = "release-variants"
	// LanePrivate: each demo's pull request, then its release, with kaniko;
	// private repositories. On GitLab each demo's merge-request gate, then
	// the release of each demo that ships a GitLab release build.
	LanePrivate = "private"
	// LanePrivateAndroid: the merge-request gates of the Android app and the
	// Android library, apart from LanePrivate because their Android runtime
	// image is prepared only on request.
	LanePrivateAndroid = "private-android"
	// LaneBaseGraph: the release of a base image and two sibling flavors
	// built on it as one stage, each staged, signed and promoted, with
	// Buildah, and on GitLab a second release that reuses them.
	LaneBaseGraph = "base-graph"
	// LaneDistroPackages: the release of a demo whose build makes .deb, .rpm
	// and .apk packages, each signed with the release's OpenPGP key and
	// published to the owner's Debian, RPM and Alpine registries. Forgejo
	// alone: the other forges refuse the declaration.
	LaneDistroPackages = "distro-packages"
	// LaneGPGFiles: the release of a demo whose declaration says sign.method
	// gpg: its checksums and SBOMs signed with the release's GPG key, pinned
	// by fingerprint, and its provenance, evidence and image with the Cosign
	// key. Forgejo and GitLab: GitHub's is a cell of its own table.
	LaneGPGFiles = "gpg-files"
)

const (
	flavorGo   = "go"
	flavorJava = "java"
	flavorJS   = "javascript-typescript"
	flavorRust = "rust"

	nativePlatform = "linux/amd64"

	// mavenServiceBuildSBOMDeclaration is the Maven service demo's variant
	// declaration: artifact-first, with the producer's Build SBOM and no SBOM
	// of the image.
	mavenServiceBuildSBOMDeclaration = ".forge-ci/artifacts-build-sbom.yml"
)

// ForgejoPreparedCases are the Forgejo consumer trials of the shipped demos.
// TestForgejoLayer_ConsumerTrial runs the pull-request and release cases,
// TestForgejoLayer_ConsumerLifecycle the lifecycles and
// TestForgejoLayer_Snapshot the snapshots, each as the subtest Name names.
var ForgejoPreparedCases = []PreparedCase{
	{Lane: LaneLifecycle, Demo: "hello-maven-service", Event: TrialLifecycle, JobImage: flavorJava, Builder: ImageBuilderBuildah, Platforms: nativePlatform, Set: SetFull},
	{Lane: LaneLifecycle, Demo: "hello-maven-service", Event: TrialLifecycle, JobImage: flavorJava, Builder: ImageBuilderKaniko, Platforms: nativePlatform, Set: SetMedium, Half: visibilityPrivate},
	{Lane: LaneLifecycle, Demo: "hello-cargo-container", Event: TrialLifecycle, JobImage: flavorRust, Builder: ImageBuilderBuildah, Platforms: nativePlatform, Set: SetFull},
	{Lane: LaneLifecycle, Demo: "hello-cargo-container", Event: TrialLifecycle, JobImage: flavorRust, Builder: ImageBuilderKaniko, Platforms: nativePlatform, Set: SetMedium},
	{Lane: LaneLifecycle, Demo: "hello-npm-service", Event: TrialLifecycle, JobImage: flavorJS, Builder: ImageBuilderBuildah, Platforms: nativePlatform, Set: SetMedium, Half: visibilityPrivate},
	{Lane: LaneLifecycle, Demo: "hello-npm-service", Event: TrialLifecycle, JobImage: flavorJS, Builder: ImageBuilderKaniko, Platforms: nativePlatform, Set: SetFull},
	{Lane: LaneLifecycle, Demo: "hello-go-service", Event: TrialLifecycle, JobImage: flavorGo, Builder: ImageBuilderBuildah, Platforms: nativePlatform, Set: SetMedium, Half: visibilityPrivate},
	{Lane: LaneLifecycle, Demo: "hello-go-service", Event: TrialLifecycle, JobImage: flavorGo, Builder: ImageBuilderKaniko, Platforms: nativePlatform, Set: SetFull},

	{Lane: LaneSnapshot, Demo: "hello-cargo-workspace", Event: TrialSnapshot, JobImage: flavorRust, Builder: ImageBuilderBuildah, Set: SetMedium},
	// Two producers of two families, each built in its own image: the
	// backend in the Java job image, the frontend in the Node one.
	{Lane: LaneSnapshot, Demo: "hello-monorepo", Event: TrialSnapshot, JobImage: flavorJava, Builder: ImageBuilderBuildah, NodeJobImage: flavorJS, Set: SetMedium},
	// A service's snapshot alone, as on GitLab: the small set's development
	// build, which hosted GitHub runs as the demo's own. Public below the
	// full set, beside the private Buildah lifecycle of the demo.
	{Lane: LaneSnapshot, Demo: "hello-go-service", Event: TrialSnapshot, JobImage: flavorGo, Builder: ImageBuilderBuildah, Set: SetSmall, Half: visibilityPublic},
	// A declaration without a container: the snapshot keeps its files,
	// through the demo's own snapshot-files caller (ForgejoSnapshotRoute).
	{Lane: LaneSnapshot, Demo: "hello-go-cli", Event: TrialSnapshot, JobImage: flavorGo, Builder: ImageBuilderBuildah, Set: SetMedium},
	// The same for an npm package, whose bundle is its one packed tarball.
	{Lane: LaneSnapshot, Demo: "hello-npm-package", Event: TrialSnapshot, JobImage: flavorJS, Builder: ImageBuilderBuildah, Set: SetMedium},
	// The Build SBOM alone: the demo's variant declaration keeps the
	// producer's Build SBOM and requests no other, so the image's retained
	// evidence must hold its scan and no SBOM (SnapshotEvidenceFindings).
	{Lane: LaneSnapshot, Demo: "hello-maven-service", Event: TrialSnapshot, JobImage: flavorJava, Builder: ImageBuilderBuildah, SnapshotDeclaration: mavenServiceBuildSBOMDeclaration, Set: SetMedium},

	{Lane: LaneReleaseVariants, Demo: "hello-go-cli", Event: TrialRelease, JobImage: flavorGo, Builder: ImageBuilderBuildah, NextVersion: "0.0.2", Platforms: nativePlatform, Set: SetFull},
	// The demo's two releases in a row are also its retention case: the
	// second release trims the first, and is otherwise the release it was.
	{Lane: LaneReleaseVariants, Demo: "hello-cargo-container", Event: TrialRelease, JobImage: flavorRust, Builder: ImageBuilderBuildah, Version: "0.2.0", NextVersion: "0.2.1", Platforms: nativePlatform, Option: OptionRetention, Set: SetMedium, Half: visibilityPrivate},
	{Lane: LaneReleaseVariants, Demo: "hello-npm-package", Event: TrialRelease, JobImage: flavorJS, Builder: ImageBuilderBuildah, Version: "0.2.0", NextVersion: "0.2.1", Platforms: nativePlatform, Set: SetMedium, Half: visibilityPublic},
	{Lane: LaneReleaseVariants, Demo: "hello-maven", Event: TrialRelease, JobImage: flavorJava, Builder: ImageBuilderBuildah, NextVersion: "0.0.2", Platforms: nativePlatform, Set: SetMedium, Half: visibilityPrivate},
	{Lane: LaneReleaseVariants, Demo: "hello-go-service", Event: TrialRelease, JobImage: flavorGo, Builder: ImageBuilderBuildah, ReleaseImagesConfig: ".forge-ci/images.yml", Platforms: nativePlatform, Set: SetMedium, Half: visibilityPublic},
	{Lane: LaneReleaseVariants, Demo: "hello-cargo-workspace", Event: TrialRelease, JobImage: flavorRust, Builder: ImageBuilderBuildah, Version: "0.2.0", ReleaseImagesConfig: ".forge-ci/release-images.yml", Platforms: nativePlatform, Set: SetMedium, Half: visibilityPublic},
	// One release of files, a package and an image: the version is this
	// case's alone, because the owner's registry takes a package version once
	// and the private lane releases the demo at 0.2.0.
	{Lane: LaneReleaseVariants, Demo: "hello-npm-service", Event: TrialRelease, JobImage: flavorJS, Builder: ImageBuilderBuildah, Version: "0.3.0", ReleaseImagesConfig: ".forge-ci/artifacts.yml", Platforms: nativePlatform, Set: SetMedium, Half: visibilityPrivate},
	// The one pull request with Buildah: the trial names the runner that
	// permits user namespaces for its image builds, where every other
	// pull request selects kaniko.
	{Lane: LaneReleaseVariants, Demo: "hello-go-service", Event: TrialPullRequest, JobImage: flavorGo, Builder: ImageBuilderBuildah, Set: SetFull},
	// One release with each further release option the Forgejo workflows
	// take, on the demo whose release workflow reads them from repository
	// variables.
	{Lane: LaneReleaseVariants, Demo: "hello-cargo-container", Event: TrialRelease, JobImage: flavorRust, Builder: ImageBuilderBuildah, Version: "0.2.0", Platforms: nativePlatform, Option: OptionDraft, Set: SetMedium, Half: visibilityPublic},
	{Lane: LaneReleaseVariants, Demo: "hello-cargo-container", Event: TrialRelease, JobImage: flavorRust, Builder: ImageBuilderBuildah, Version: "0.3.0-rc.1", Platforms: nativePlatform, Option: OptionPrerelease, Set: SetMedium, Half: visibilityPrivate},

	{Lane: LanePrivate, Demo: "hello-go-cli", Event: TrialPullRequest, JobImage: flavorGo, Builder: ImageBuilderKaniko, Set: SetSmall},
	{Lane: LanePrivate, Demo: "hello-go-cli", Event: TrialRelease, JobImage: flavorGo, Builder: ImageBuilderKaniko, Platforms: nativePlatform, Set: SetMedium},
	{Lane: LanePrivate, Demo: "hello-go-service", Event: TrialPullRequest, JobImage: flavorGo, Builder: ImageBuilderKaniko, Set: SetFull},
	{Lane: LanePrivate, Demo: "hello-go-service", Event: TrialRelease, JobImage: flavorGo, Builder: ImageBuilderKaniko, Platforms: nativePlatform, Set: SetMedium},
	{Lane: LanePrivate, Demo: "hello-maven-service", Event: TrialPullRequest, JobImage: flavorJava, Builder: ImageBuilderKaniko, Set: SetFull},
	{Lane: LanePrivate, Demo: "hello-maven-service", Event: TrialRelease, JobImage: flavorJava, Builder: ImageBuilderKaniko, Platforms: nativePlatform, Set: SetSmall},
	{Lane: LanePrivate, Demo: "hello-npm-service", Event: TrialPullRequest, JobImage: flavorJS, Builder: ImageBuilderKaniko, Set: SetFull},
	{Lane: LanePrivate, Demo: "hello-npm-service", Event: TrialRelease, JobImage: flavorJS, Builder: ImageBuilderKaniko, Version: "0.2.0", Platforms: nativePlatform, Set: SetMedium},
	{Lane: LanePrivate, Demo: "hello-maven", Event: TrialPullRequest, JobImage: flavorJava, Builder: ImageBuilderKaniko, Set: SetSmall},
	{Lane: LanePrivate, Demo: "hello-maven", Event: TrialRelease, JobImage: flavorJava, Builder: ImageBuilderKaniko, Platforms: nativePlatform, Set: SetMedium},
	{Lane: LanePrivate, Demo: "hello-cargo-container", Event: TrialPullRequest, JobImage: flavorRust, Builder: ImageBuilderKaniko, Set: SetFull},
	{Lane: LanePrivate, Demo: "hello-cargo-container", Event: TrialRelease, JobImage: flavorRust, Builder: ImageBuilderKaniko, Version: "0.2.0", Platforms: nativePlatform, Set: SetFull},
	{Lane: LanePrivate, Demo: "hello-cargo-workspace", Event: TrialPullRequest, JobImage: flavorRust, Builder: ImageBuilderKaniko, Set: SetMedium},
	{Lane: LanePrivate, Demo: "hello-cargo-workspace", Event: TrialRelease, JobImage: flavorRust, Builder: ImageBuilderKaniko, Version: "0.2.0", Platforms: nativePlatform, Set: SetMedium},
	{Lane: LanePrivate, Demo: "hello-npm-package", Event: TrialPullRequest, JobImage: flavorJS, Builder: ImageBuilderKaniko, Set: SetSmall},
	{Lane: LanePrivate, Demo: "hello-npm-package", Event: TrialRelease, JobImage: flavorJS, Builder: ImageBuilderKaniko, Version: "0.2.0", Platforms: nativePlatform, Set: SetMedium},
	{Lane: LanePrivate, Demo: "hello-gradle", Event: TrialPullRequest, JobImage: flavorJava, Builder: ImageBuilderKaniko, Set: SetSmall},
	{Lane: LanePrivate, Demo: "hello-gradle", Event: TrialRelease, JobImage: flavorJava, Builder: ImageBuilderKaniko, Platforms: nativePlatform, Set: SetMedium},
	// The monorepo's gate checks its backend in the Java job image and its
	// frontend in the Node one; its release bumps and builds in the images its
	// workflow pins. The release is one version of both: the jar and the
	// package as files, an image of each, and the package in the owner's
	// registry, which takes a version once.
	{Lane: LanePrivate, Demo: "hello-monorepo", Event: TrialPullRequest, JobImage: flavorJava, Builder: ImageBuilderKaniko, NodeJobImage: flavorJS, Set: SetMedium},
	{Lane: LanePrivate, Demo: "hello-monorepo", Event: TrialRelease, JobImage: flavorJava, Builder: ImageBuilderKaniko, Version: "0.2.0", Platforms: nativePlatform, Set: SetMedium},
	// hello-meta builds nothing and needs no toolchain: any prepared flavor
	// carries its checks, and the Rust one has run this set of them.
	{Lane: LanePrivate, Demo: "hello-meta", Event: TrialPullRequest, JobImage: flavorRust, Builder: ImageBuilderKaniko, Set: SetSmall},
	{Lane: LanePrivate, Demo: "hello-meta", Event: TrialRelease, JobImage: flavorRust, Builder: ImageBuilderKaniko, Platforms: nativePlatform, Set: SetMedium},
	// A multi-module build declared once at its root: a Maven reactor. The npm
	// workspace is hello-npm-package, whose root bundles its member.
	{Lane: LanePrivate, Demo: "hello-maven-reactor", Event: TrialPullRequest, JobImage: flavorJava, Builder: ImageBuilderKaniko, Set: SetMedium},
	{Lane: LanePrivate, Demo: "hello-maven-reactor", Event: TrialRelease, JobImage: flavorJava, Builder: ImageBuilderKaniko, Platforms: nativePlatform, Set: SetMedium},
	// Cargo without a container: a command and the library crate it links,
	// compiled by cargo in the build job.
	{Lane: LanePrivate, Demo: "hello-cargo-cli", Event: TrialPullRequest, JobImage: flavorRust, Builder: ImageBuilderKaniko, Set: SetSmall},
	{Lane: LanePrivate, Demo: "hello-cargo-cli", Event: TrialRelease, JobImage: flavorRust, Builder: ImageBuilderKaniko, Version: "0.2.0", Platforms: nativePlatform, Set: SetMedium},
	// A Gradle service: the jar and the runtime image built from it, with the
	// development route of an artifact-first snapshot.
	{Lane: LaneLifecycle, Demo: "hello-gradle-service", Event: TrialLifecycle, JobImage: flavorJava, Builder: ImageBuilderBuildah, Platforms: nativePlatform, Set: SetMedium, Half: visibilityPublic},
	{Lane: LaneLifecycle, Demo: "hello-gradle-service", Event: TrialLifecycle, JobImage: flavorJava, Builder: ImageBuilderKaniko, Platforms: nativePlatform, Set: SetFull},
	{Lane: LanePrivate, Demo: "hello-gradle-service", Event: TrialPullRequest, JobImage: flavorJava, Builder: ImageBuilderKaniko, Set: SetMedium},
	{Lane: LanePrivate, Demo: "hello-gradle-service", Event: TrialRelease, JobImage: flavorJava, Builder: ImageBuilderKaniko, Platforms: nativePlatform, Set: SetMedium},

	// The base-image graph is a release alone: a root, then a flavor on the
	// parent the release authenticated. Its own jobs take no job image; the
	// row names a flavor because every case's settings carry one.
	// Buildah alone: the engine refuses to build on a prepared private parent
	// with kaniko.
	{Lane: LaneBaseGraph, Demo: "hello-base-images", Event: TrialRelease, JobImage: flavorGo, Builder: ImageBuilderBuildah, Set: SetMedium},

	// The Go CLI released with the example's GoReleaser alternative, which
	// the demo keeps as fixtures: archives and a .deb, .rpm and .apk package
	// of each architecture, signed and published to the owner's registries.
	// The version is this case's alone: those registries take a version
	// once. The build runs on the runner and builds no image; the row names
	// a flavor and a builder because every case's settings carry them.
	{Lane: LaneDistroPackages, Demo: "hello-go-cli", Event: TrialRelease, JobImage: flavorGo, Builder: ImageBuilderBuildah, Version: "0.4.0", Platforms: nativePlatform, Fixtures: ForgejoPackagesFixtures, Set: SetMedium, Half: visibilityPrivate},

	// The Go service released from its declaration with sign.method gpg,
	// which the demo keeps as fixtures. The engine refuses to load the GPG
	// key on a runner with swap on, so the lane runs where the runners have
	// none, as the distro packages' does.
	{Lane: LaneGPGFiles, Demo: "hello-go-service", Event: TrialRelease, JobImage: flavorGo, Builder: ImageBuilderBuildah, Platforms: nativePlatform, Fixtures: ForgejoGPGFilesFixtures, Set: SetMedium, Half: visibilityPublic},
}

// GitLabPreparedCases are the GitLab consumer trials of the shipped demos.
// TestGitLabLayer_ConsumerTrial runs the pull-request cases,
// TestGitLabRelease_ConsumerTrial the releases,
// TestGitLabLayer_ConsumerLifecycle the lifecycles and
// TestGitLabLayer_Snapshot the snapshots, each as the subtest Name names. A
// lifecycle and a release request Version and run their job-image jobs as the
// runtime images' nonroot user. A lifecycle and a snapshot run with either
// builder, a case each.
var GitLabPreparedCases = []PreparedCase{
	{Lane: LaneLifecycle, Demo: "hello-maven-service", Event: TrialLifecycle, JobImage: flavorJava, Builder: ImageBuilderBuildah, Version: "0.2.0", Set: SetFull},
	{Lane: LaneLifecycle, Demo: "hello-maven-service", Event: TrialLifecycle, JobImage: flavorJava, Builder: ImageBuilderKaniko, Version: "0.2.0", Set: SetMedium, Half: visibilityPublic},
	{Lane: LaneLifecycle, Demo: "hello-cargo-container", Event: TrialLifecycle, JobImage: flavorRust, Builder: ImageBuilderBuildah, Version: "0.2.0", Set: SetMedium, Half: visibilityPublic},
	{Lane: LaneLifecycle, Demo: "hello-cargo-container", Event: TrialLifecycle, JobImage: flavorRust, Builder: ImageBuilderKaniko, Version: "0.2.0", Set: SetMedium, Half: visibilityPrivate},
	{Lane: LaneLifecycle, Demo: "hello-npm-service", Event: TrialLifecycle, JobImage: flavorJS, Builder: ImageBuilderBuildah, Version: "0.2.0", Set: SetMedium, Half: visibilityPrivate},
	{Lane: LaneLifecycle, Demo: "hello-npm-service", Event: TrialLifecycle, JobImage: flavorJS, Builder: ImageBuilderKaniko, Version: "0.2.0", Set: SetFull},

	{Lane: LaneSnapshot, Demo: "hello-cargo-workspace", Event: TrialSnapshot, JobImage: flavorRust, Builder: ImageBuilderBuildah, Set: SetMedium},
	{Lane: LaneSnapshot, Demo: "hello-cargo-workspace", Event: TrialSnapshot, JobImage: flavorRust, Builder: ImageBuilderKaniko, Set: SetMedium},
	{Lane: LaneSnapshot, Demo: "hello-monorepo", Event: TrialSnapshot, JobImage: flavorJava, Builder: ImageBuilderBuildah, Set: SetMedium},
	{Lane: LaneSnapshot, Demo: "hello-monorepo", Event: TrialSnapshot, JobImage: flavorJava, Builder: ImageBuilderKaniko, Set: SetMedium},
	{Lane: LaneSnapshot, Demo: "hello-go-service", Event: TrialSnapshot, JobImage: flavorGo, Builder: ImageBuilderBuildah, Set: SetSmall},
	{Lane: LaneSnapshot, Demo: "hello-go-service", Event: TrialSnapshot, JobImage: flavorGo, Builder: ImageBuilderKaniko, Set: SetMedium},
	// A declaration without a container: the snapshot keeps its files.
	{Lane: LaneSnapshot, Demo: "hello-go-cli", Event: TrialSnapshot, JobImage: flavorGo, Builder: ImageBuilderBuildah, Set: SetMedium},
	// The same for an npm package, whose bundle is its one packed tarball.
	{Lane: LaneSnapshot, Demo: "hello-npm-package", Event: TrialSnapshot, JobImage: flavorJS, Builder: ImageBuilderBuildah, Set: SetMedium},
	// The Build SBOM alone, as on Forgejo.
	{Lane: LaneSnapshot, Demo: "hello-maven-service", Event: TrialSnapshot, JobImage: flavorJava, Builder: ImageBuilderBuildah, SnapshotDeclaration: mavenServiceBuildSBOMDeclaration, Set: SetMedium},

	{Lane: LanePrivate, Demo: "hello-go-cli", Event: TrialPullRequest, JobImage: flavorGo, Builder: ImageBuilderKaniko, Set: SetSmall},
	{Lane: LanePrivate, Demo: "hello-maven", Event: TrialPullRequest, JobImage: flavorJava, Builder: ImageBuilderKaniko, Set: SetSmall},
	{Lane: LanePrivate, Demo: "hello-npm-service", Event: TrialPullRequest, JobImage: flavorJS, Builder: ImageBuilderKaniko, Set: SetMedium},
	{Lane: LanePrivate, Demo: "hello-cargo-container", Event: TrialPullRequest, JobImage: flavorRust, Builder: ImageBuilderKaniko, Set: SetMedium},
	{Lane: LanePrivate, Demo: "hello-go-service", Event: TrialPullRequest, JobImage: flavorGo, Builder: ImageBuilderKaniko, Set: SetMedium},
	{Lane: LanePrivate, Demo: "hello-maven-service", Event: TrialPullRequest, JobImage: flavorJava, Builder: ImageBuilderKaniko, Set: SetMedium},
	{Lane: LanePrivate, Demo: "hello-gradle", Event: TrialPullRequest, JobImage: flavorJava, Builder: ImageBuilderKaniko, Set: SetSmall},
	{Lane: LanePrivate, Demo: "hello-monorepo", Event: TrialPullRequest, JobImage: flavorJava, Builder: ImageBuilderKaniko, Set: SetMedium},
	{Lane: LanePrivate, Demo: "hello-cargo-workspace", Event: TrialPullRequest, JobImage: flavorRust, Builder: ImageBuilderKaniko, Set: SetMedium},
	{Lane: LanePrivate, Demo: "hello-npm-package", Event: TrialPullRequest, JobImage: flavorJS, Builder: ImageBuilderKaniko, Set: SetSmall},
	// As on Forgejo, the Rust flavor stands in: hello-meta needs no toolchain.
	{Lane: LanePrivate, Demo: "hello-meta", Event: TrialPullRequest, JobImage: flavorRust, Builder: ImageBuilderKaniko, Set: SetSmall},

	{Lane: LanePrivate, Demo: "hello-maven", Event: TrialRelease, JobImage: flavorJava, Builder: ImageBuilderKaniko, Version: "0.2.0", Set: SetMedium},
	{Lane: LanePrivate, Demo: "hello-npm-service", Event: TrialRelease, JobImage: flavorJS, Builder: ImageBuilderKaniko, Version: "0.2.0", Set: SetMedium},
	{Lane: LanePrivate, Demo: "hello-maven-service", Event: TrialRelease, JobImage: flavorJava, Builder: ImageBuilderKaniko, Version: "0.2.0", Set: SetSmall},
	{Lane: LanePrivate, Demo: "hello-cargo-container", Event: TrialRelease, JobImage: flavorRust, Builder: ImageBuilderKaniko, Version: "0.2.0", Set: SetMedium},
	{Lane: LanePrivate, Demo: "hello-cargo-workspace", Event: TrialRelease, JobImage: flavorRust, Builder: ImageBuilderKaniko, Version: "0.2.0", Set: SetMedium},
	{Lane: LanePrivate, Demo: "hello-npm-package", Event: TrialRelease, JobImage: flavorJS, Builder: ImageBuilderKaniko, Version: "0.2.0", Set: SetMedium},
	{Lane: LanePrivate, Demo: "hello-gradle", Event: TrialRelease, JobImage: flavorJava, Builder: ImageBuilderKaniko, Version: "0.2.0", Set: SetMedium},
	{Lane: LanePrivate, Demo: "hello-go-cli", Event: TrialRelease, JobImage: flavorGo, Builder: ImageBuilderKaniko, Version: "0.2.0", Set: SetMedium},
	{Lane: LanePrivate, Demo: "hello-meta", Event: TrialRelease, JobImage: flavorRust, Builder: ImageBuilderKaniko, Version: "0.2.0", Set: SetMedium},
	// The monorepo, as on Forgejo: the plan's java and node families, each
	// bumped and built in its own runtime image, the Node one the further
	// archive the gate's $CI_JOB_IMAGE_NODE already takes.
	{Lane: LanePrivate, Demo: "hello-monorepo", Event: TrialRelease, JobImage: flavorJava, Builder: ImageBuilderKaniko, Version: "0.2.0", Set: SetMedium},
	// The Maven reactor, as on Forgejo.
	{Lane: LanePrivate, Demo: "hello-maven-reactor", Event: TrialPullRequest, JobImage: flavorJava, Builder: ImageBuilderKaniko, Set: SetMedium},
	{Lane: LanePrivate, Demo: "hello-maven-reactor", Event: TrialRelease, JobImage: flavorJava, Builder: ImageBuilderKaniko, Version: "0.2.0", Set: SetMedium},
	// Cargo without a container, as on Forgejo: the build job of the Rust
	// family compiles the command.
	{Lane: LanePrivate, Demo: "hello-cargo-cli", Event: TrialPullRequest, JobImage: flavorRust, Builder: ImageBuilderKaniko, Set: SetSmall},
	{Lane: LanePrivate, Demo: "hello-cargo-cli", Event: TrialRelease, JobImage: flavorRust, Builder: ImageBuilderKaniko, Version: "0.2.0", Set: SetMedium},
	// A Gradle service, as on Forgejo: either builder for the lifecycle.
	{Lane: LaneLifecycle, Demo: "hello-gradle-service", Event: TrialLifecycle, JobImage: flavorJava, Builder: ImageBuilderBuildah, Version: "0.2.0", Set: SetMedium, Half: visibilityPublic},
	{Lane: LaneLifecycle, Demo: "hello-gradle-service", Event: TrialLifecycle, JobImage: flavorJava, Builder: ImageBuilderKaniko, Version: "0.2.0", Set: SetFull},
	{Lane: LanePrivate, Demo: "hello-gradle-service", Event: TrialPullRequest, JobImage: flavorJava, Builder: ImageBuilderKaniko, Set: SetMedium},
	{Lane: LanePrivate, Demo: "hello-gradle-service", Event: TrialRelease, JobImage: flavorJava, Builder: ImageBuilderKaniko, Version: "0.2.0", Set: SetMedium},

	// The Android gates build in the Android runtime image and lint in the
	// Java flavor: both demos are Java, and their plans run no Kotlin check.
	{Lane: LanePrivateAndroid, Demo: "hello-android", Event: TrialPullRequest, JobImage: flavorJava, Builder: ImageBuilderKaniko, Runtime: "android", Set: SetMedium},
	{Lane: LanePrivateAndroid, Demo: "hello-android-library", Event: TrialPullRequest, JobImage: flavorJava, Builder: ImageBuilderKaniko, Runtime: "android", Set: SetMedium},

	// The release options whose GitLab route is its own: a prerelease is a
	// note on the release, GitLab having no flag for one, and retention
	// removes an upload and its link. The policy takes each as an input, so
	// any demo with a release build serves; this one releases files alone.
	{Lane: LaneReleaseVariants, Demo: "hello-go-cli", Event: TrialRelease, JobImage: flavorGo, Builder: ImageBuilderBuildah, Version: "0.3.0-rc.1", Option: OptionPrerelease, Set: SetMedium, Half: visibilityPublic},
	{Lane: LaneReleaseVariants, Demo: "hello-go-cli", Event: TrialRelease, JobImage: flavorGo, Builder: ImageBuilderBuildah, Version: "0.2.0", NextVersion: "0.2.1", Option: OptionRetention, Set: SetMedium, Half: visibilityPrivate},

	// One release of files, a package and an image, as on Forgejo: the
	// release's package job publishes the packed package to the project's own
	// registry, which every release trial reads. Buildah, where the private
	// lane's release of the demo builds with kaniko.
	{Lane: LaneReleaseVariants, Demo: "hello-npm-service", Event: TrialRelease, JobImage: flavorJS, Builder: ImageBuilderBuildah, Version: "0.3.0", Set: SetMedium, Half: visibilityPublic},

	// The base-image graph, as on Forgejo: the policy's base stages build,
	// sign and promote the root, then its two flavors on the parent a signer
	// job verified, and the release's file names all three by digest. Buildah
	// alone: a child stage is refused with kaniko. The planner and the build
	// script need no toolchain; the Go flavor carries them. The second
	// release changes no base input, so it reuses every stage the first
	// promoted, verified again and signed no further.
	{Lane: LaneBaseGraph, Demo: "hello-base-images", Event: TrialRelease, JobImage: flavorGo, Builder: ImageBuilderBuildah, Version: "0.2.0", NextVersion: "0.2.1", Set: SetMedium},

	// The Go CLI released from its declaration with sign.method gpg, which
	// the demo keeps as fixtures, as on Forgejo, where the Go service is. The
	// GitLab release needs a demo with a release build, which the Go service
	// has not. The engine refuses to load the GPG key on a runner with swap
	// on, so the lane runs where the signer runners have none.
	{Lane: LaneGPGFiles, Demo: "hello-go-cli", Event: TrialRelease, JobImage: flavorGo, Builder: ImageBuilderBuildah, Version: "0.2.0", Fixtures: GitLabGPGFilesFixtures, Set: SetMedium, Half: visibilityPublic},
}

// PreparedCases are the cases of a lab forge's table, none for another.
func PreparedCases(forge string) []PreparedCase {
	switch forge {
	case forgeForgejo:
		return ForgejoPreparedCases
	case forgeGitLab:
		return GitLabPreparedCases
	default:
		return nil
	}
}

// preparedCaseTests name the live test that runs a forge's cases of an event.
var preparedCaseTests = map[string]map[string]string{
	forgeForgejo: {
		TrialPullRequest: "TestForgejoLayer_ConsumerTrial", TrialRelease: "TestForgejoLayer_ConsumerTrial",
		TrialLifecycle: "TestForgejoLayer_ConsumerLifecycle", TrialSnapshot: "TestForgejoLayer_Snapshot",
	},
	forgeGitLab: {
		TrialPullRequest: "TestGitLabLayer_ConsumerTrial", TrialRelease: gitlabConsumerTrialTest,
		TrialLifecycle: "TestGitLabLayer_ConsumerLifecycle", TrialSnapshot: "TestGitLabLayer_Snapshot",
	},
}

// PreparedCaseTest names the live test that runs a forge's cases of event,
// each as the subtest <test>/<case name>.
func PreparedCaseTest(forge, event string) string {
	return preparedCaseTests[forge][event]
}

// Name is the case's subtest name, <lane>/<demo>/<event>/<builder> and, for
// a case with a release option, /<option>, one per case. `-run` matches it
// element by element, so a pattern that ends at the builder also selects the
// option cases of that demo and builder; one that goes on to the visibility
// selects the case without an option alone.
func (c PreparedCase) Name() string {
	elements := []string{c.Lane, c.Demo, c.Event, c.Builder}
	if c.Option != "" {
		elements = append(elements, c.Option)
	}

	return strings.Join(elements, "/")
}

// LaneEnvironment is what a private lane fixes for its cases on forge, so a
// run cannot leave it out: private repositories alone, and on GitLab the
// administrator's reproducible-build check turned on. Another lane fixes
// nothing; its visibility and administrator inputs are the caller's.
func (c PreparedCase) LaneEnvironment(forge string) map[string]string {
	if c.Lane != LanePrivate && c.Lane != LanePrivateAndroid {
		return nil
	}

	environment := map[string]string{consumerVisibilityEnv: visibilityPrivate}
	if forge == forgeGitLab {
		environment[adminInputEnvironment(enforcement.ReproducibleBuilds)] = variableTrue
	}

	return environment
}

// Settings are the trial's settings of the case, one key=value each, with the
// images taken from prepared, the prepared directory's prepared.env. A case
// whose image was not prepared is refused, naming the key that is missing.
// A case that builds with kaniko (BuildsWithKaniko) needs the saved kaniko
// job image archive; the run gives CI_IMAGE_BUILDER_JOB_IMAGE itself.
func (c PreparedCase) Settings(prepared map[string]string) ([]string, error) {
	jobImage := preparedJobImagePrefix + flavorEnvKey(c.JobImage)
	if prepared[jobImage] == "" {
		return nil, c.notPrepared(jobImage)
	}

	settings := []string{"CI_JOB_IMAGE=" + prepared[jobImage], "CI_IMAGE_BUILDER=" + c.Builder}

	if c.NodeJobImage != "" {
		nodeImage := preparedJobImagePrefix + flavorEnvKey(c.NodeJobImage)
		if prepared[nodeImage] == "" {
			return nil, c.notPrepared(nodeImage)
		}

		settings = append(settings, "CI_JOB_IMAGE_NODE="+prepared[nodeImage])
	}

	if c.BuildsWithKaniko() && prepared[KanikoJobImageArchiveEnv] == "" {
		return nil, c.notPrepared(KanikoJobImageArchiveEnv)
	}

	if c.Version != "" {
		settings = append(settings, settingVersion+"="+c.Version)
	}

	if c.NextVersion != "" {
		settings = append(settings, settingNextVersion+"="+c.NextVersion)
	}

	if c.ReleaseImagesConfig != "" {
		settings = append(settings, settingReleaseImagesConfig+"="+c.ReleaseImagesConfig)
	}

	if c.Platforms != "" {
		settings = append(settings, "RELEASE_PLATFORMS="+c.Platforms)
	}

	if c.Option != "" {
		settings = append(settings, settingReleaseOption+"="+c.Option)
	}

	if c.Fixtures != "" {
		settings = append(settings, settingFixtures+"="+c.Fixtures)
	}

	if c.SnapshotDeclaration != "" {
		settings = append(settings, settingSnapshotDeclaration+"="+c.SnapshotDeclaration)
	}

	return settings, nil
}

// PreparedForgejoEnvironment is what a Forgejo consumer trial reads from its
// environment and the prepared directory's prepared.env holds: the signer
// image and the builders' saved job images. One that was not prepared is
// empty, so a value left in the caller's environment is never taken for it.
// The administrator inputs (FORGE_CI_LIVE_ADMIN_<CHECK>) are the caller's.
func PreparedForgejoEnvironment(prepared map[string]string) map[string]string {
	environment := map[string]string{}

	for _, key := range []string{signerImageEnv, GitLabSnapshotBuilderArchiveEnv(ImageBuilderBuildah), KanikoJobImageArchiveEnv} {
		environment[key] = prepared[key]
	}

	return environment
}

// The archives a GitLab consumer trial reads from its environment, as
// `just test-live-consumer gitlab` names them from job-archive=,
// build-archive=, signer-archive= and lint-archive=; a merge request's job
// archive is its consumer image archive.
const (
	gitLabJobArchiveEnv      = "FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE"
	gitLabBuildArchiveEnv    = "FORGE_CI_LIVE_BUILD_IMAGE_ARCHIVE"
	gitLabSignerArchiveEnv   = "FORGE_CI_LIVE_SIGNER_IMAGE_ARCHIVE"
	gitLabLintArchiveEnv     = "FORGE_CI_LIVE_LINT_IMAGE_ARCHIVE"
	gitLabConsumerArchiveEnv = "FORGE_CI_LIVE_CONSUMER_IMAGE_ARCHIVE"

	// The user a release's and a lifecycle's job images run as.
	gitLabReleaseJobUser = "65532:65532"
)

// gitLabRuntimeTarget is the runtime image target a flavor's demos build and
// check in on GitLab, the <TARGET> of GitLabJobImageArchiveEnv in capitals.
var gitLabRuntimeTarget = map[string]string{flavorGo: "go", flavorJava: "java", flavorJS: "node", flavorRust: "rust"}

// GitLabSettings are the trial's settings of the case on GitLab, one
// key=value each: a release's and a lifecycle's version and job user, a
// snapshot's declaration where the case names one, and none for a merge
// request, which takes its images alone. A release names no scenario, so it
// is the consumer release trial (GitLabConsumerReleaseScenario); it adds its
// second version, its release option and its fixtures, each left out when
// empty.
func (c PreparedCase) GitLabSettings() []string {
	if c.Event == TrialSnapshot && c.SnapshotDeclaration != "" {
		return []string{settingSnapshotDeclaration + "=" + c.SnapshotDeclaration}
	}

	if c.Event != TrialLifecycle && c.Event != TrialRelease {
		return nil
	}

	settings := []string{settingVersion + "=" + c.Version, "job-user=" + gitLabReleaseJobUser}

	if c.NextVersion != "" {
		settings = append(settings, settingNextVersion+"="+c.NextVersion)
	}

	if c.Option != "" {
		settings = append(settings, settingReleaseOption+"="+c.Option)
	}

	if c.Fixtures != "" {
		settings = append(settings, settingFixtures+"="+c.Fixtures)
	}

	return settings
}

// GitLabEnvironment is what a GitLab consumer trial of the case reads from
// its environment and prepared, the prepared directory's prepared.env, holds.
// The job image is the runtime archive of the case's flavor, or of Runtime.
// A snapshot takes it alone, and builds in the saved job image of its
// builder. A merge request takes it as its consumer image and lints in the
// flavor's nanolinter archive. A release and a lifecycle take it as job and
// build image, sign with the prepared signer and lint as the merge request
// does. A missing archive is refused, naming its key; one the event does not
// take is empty, as is any that was not prepared: a value left in the
// caller's environment is never taken for one.
func (c PreparedCase) GitLabEnvironment(prepared map[string]string) (map[string]string, error) {
	environment := map[string]string{gitLabJobArchiveEnv: "", gitLabBuildArchiveEnv: "", gitLabSignerArchiveEnv: "", gitLabLintArchiveEnv: ""}

	for _, key := range []string{signerImageEnv, GitLabSnapshotBuilderArchiveEnv(ImageBuilderBuildah), KanikoJobImageArchiveEnv} {
		environment[key] = prepared[key]
	}

	// A gate's further job image takes its runtime archive by name.
	for key, value := range prepared {
		if strings.HasPrefix(key, GitLabJobImageArchiveEnv) {
			environment[key] = value
		}
	}

	job := GitLabJobImageArchiveEnv + strings.ToUpper(cmp.Or(c.Runtime, gitLabRuntimeTarget[c.JobImage]))
	if prepared[job] == "" {
		return nil, c.notPrepared(job)
	}

	if c.Event == TrialSnapshot {
		if builder := GitLabSnapshotBuilderArchiveEnv(c.Builder); prepared[builder] == "" {
			return nil, c.notPrepared(builder)
		}

		environment[gitLabJobArchiveEnv] = prepared[job]

		return environment, nil
	}

	lint := preparedLintArchivePrefix + flavorEnvKey(c.JobImage)
	if prepared[lint] == "" {
		return nil, c.notPrepared(lint)
	}

	environment[gitLabLintArchiveEnv] = prepared[lint]

	if c.Event == TrialPullRequest {
		environment[gitLabConsumerArchiveEnv] = prepared[job]

		return environment, nil
	}

	if prepared[gitLabSignerArchiveEnv] == "" {
		return nil, c.notPrepared(gitLabSignerArchiveEnv)
	}

	environment[gitLabJobArchiveEnv] = prepared[job]
	environment[gitLabBuildArchiveEnv] = prepared[job]
	environment[gitLabSignerArchiveEnv] = prepared[gitLabSignerArchiveEnv]

	return environment, nil
}

// RequireGitLabFurtherArchives refuses a GitLab case whose demo, at
// checkout, runs in a runtime image the prepared directory holds no archive
// of: a producer family of its snapshot, or a further job image of its
// gate. The trial would otherwise report itself not covered and skip.
func (c PreparedCase) RequireGitLabFurtherArchives(checkout string, prepared map[string]string) error {
	further, err := gitLabCaseFurtherImages(checkout, c)
	if err != nil {
		return fmt.Errorf("%s: %w", c.Name(), err)
	}

	for _, name := range further {
		if key := GitLabJobImageArchiveEnv + strings.ToUpper(name); prepared[key] == "" {
			return c.notPrepared(key)
		}
	}

	return nil
}

// BuildsWithKaniko is whether the case's trial builds images with kaniko: a
// release, lifecycle or snapshot whose builder is kaniko. A pull request
// builds none.
func (c PreparedCase) BuildsWithKaniko() bool {
	return c.Builder == ImageBuilderKaniko && c.Event != TrialPullRequest
}

func (c PreparedCase) notPrepared(key string) error {
	return fmt.Errorf("%s: %s has no %s; prepare its image: %w", c.Name(), PreparedEnvFile, key, errs.ErrValidation)
}
