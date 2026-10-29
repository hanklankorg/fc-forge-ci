// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/runtimetags"
	"gopkg.in/yaml.v3"
)

// TestRuntimeImageTagsShareOneVersion pins every forge-ci-runtime-*
// image reference across the workflows to a single version tag. The
// runtime images are this repo's own release artifact — Renovate never
// bumps them — so a version cut rewrites every workflow default, and a
// missed site fails silently as a stale-runtime job rather than an
// error. This guard turns that silent drift into a failing build, in
// the same guardrail-test style as workflow-contract and docs-sync.
//
// The pattern definitions are shared with cmd/bump-runtime-tags (the
// tool that moves the pins on a release cut: `just bump-runtime-tags`)
// via internal/runtimetags, so rewriter and guard cannot drift apart.
func TestRuntimeImageTagsShareOneVersion(t *testing.T) {
	t.Parallel()

	// self-runtime-container.yml builds the runtime images themselves
	// and smoke-tests candidates under a local-only ":verify" tag that
	// never leaves the job. That tag is deliberate, not drift.
	verifyTagAllowedFiles := map[string]bool{
		".github/workflows/self-runtime-container.yml": true,
	}

	root := reporoot.Path(t)
	files, err := runtimetags.Files(root)
	require.NoError(t, err)

	// tag → sorted, unique "file:line" sites referencing it.
	sites := map[string][]string{}

	for _, rel := range files {
		content := reporoot.ReadFile(t, rel)
		collectRuntimeTagSites(filepath.ToSlash(rel), string(content), verifyTagAllowedFiles, sites)
	}

	require.NotEmpty(t, sites, "no forge-ci-runtime image references found under .github/workflows — pattern or layout changed?")

	tags := make([]string, 0, len(sites))
	for tag := range sites {
		tags = append(tags, tag)
	}

	slices.Sort(tags)

	if len(tags) > 1 {
		var report strings.Builder
		for _, tag := range tags {
			report.WriteString("\n  " + tag + ":\n    " + strings.Join(sites[tag], "\n    "))
		}

		require.Fail(t, "runtime image tags diverged",
			"every forge-ci-runtime-* reference must carry the same version tag; a version cut must rewrite all of them together.%s", report.String())
	}

	require.Truef(t, runtimetags.ValidVersion(tags[0]),
		"the shared runtime image tag %q is not a release version (vX.Y.Z); sites:\n  %s",
		tags[0], strings.Join(sites[tags[0]], "\n  "))
}

// collectRuntimeTagSites records every runtime-image tag in content as
// "rel:line" under its tag, skipping the local-only ":verify" tag in
// allowlisted files.
func collectRuntimeTagSites(rel, content string, verifyTagAllowedFiles map[string]bool, sites map[string][]string) {
	for index, line := range strings.Split(content, "\n") {
		for _, tag := range runtimetags.Tags(line) {
			// The resolver's printf template is executed by the bootstrap contract
			// tests; its format placeholder is not a literal OCI version pin.
			if rel == ".github/workflows/resolve-forge-ci-bootstrap.yml" && tag == `%s\n` {
				continue
			}
			if tag == "verify" && verifyTagAllowedFiles[rel] {
				continue
			}

			if selfGraphImageTag(rel, tag) {
				continue
			}

			sites[tag] = append(sites[tag], rel+":"+strconv.Itoa(index+1))
		}
	}
}

// selfGraphImageTag reports whether tag is one of the images this repository's
// own PR and release gates consume instead of a released version: the rolling
// v0.90.0-pre line Self Runtime Container publishes, used by Self Pull Request, and
// the release commit's own sha-<short> tag, which Self Release builds with an
// expression. Neither is a default a version cut rewrites.
func selfGraphImageTag(rel, tag string) bool {
	switch rel {
	case ".github/workflows/self-pullrequest.yml":
		// TestSelfPullRequestUsesThePublishedRollingImageTag pins the exact value.
		return rollingImageTag.MatchString(tag)
	case ".github/workflows/self-release.yml":
		return tag == "$"
	default:
		return false
	}
}

// rollingImageTag is the shape of the rolling image line, for example v0.90.0-pre.
var rollingImageTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+-pre$`)

// TestSelfPullRequestUsesThePublishedRollingImageTag keeps the rolling tag in
// one place: Self Pull Request must consume exactly the tag Self Runtime
// Container publishes, so moving the release baseline moves both.
func TestSelfPullRequestUsesThePublishedRollingImageTag(t *testing.T) {
	t.Parallel()

	published := regexp.MustCompile(`type=raw,value=(v[0-9]+\.[0-9]+\.[0-9]+-pre),`).FindStringSubmatch(
		string(reporoot.ReadFile(t, ".github/workflows/self-runtime-container.yml")))
	require.Len(t, published, 2, "self-runtime-container.yml publishes no rolling vX.Y.Z-pre tag")

	consumed := regexp.MustCompile(`forge-ci-runtime-base:(v[0-9]+\.[0-9]+\.[0-9]+-pre)\b`).FindAllStringSubmatch(
		string(reporoot.ReadFile(t, ".github/workflows/self-pullrequest.yml")), -1)
	require.NotEmpty(t, consumed, "self-pullrequest.yml consumes no rolling runtime image")

	for _, match := range consumed {
		require.Equal(t, published[1], match[1], "Self Pull Request must use the rolling tag Self Runtime Container publishes")
	}
}

// TestRuntimeImageTagGuardCatchesDivergence proves the guard's scanner
// actually flags a drifted tag and honours the :verify allowlist, so a
// future regex or allowlist change cannot quietly blind the guard.
func TestRuntimeImageTagGuardCatchesDivergence(t *testing.T) {
	t.Parallel()

	allowed := map[string]bool{".github/workflows/self-runtime-container.yml": true}
	sites := map[string][]string{}

	collectRuntimeTagSites(".github/workflows/build-x.yml",
		`default: "ghcr.io/diggsweden/forge-ci-runtime-base:v3.0.0"`, allowed, sites)
	collectRuntimeTagSites(".github/workflows/build-y.yml",
		`default: "ghcr.io/diggsweden/forge-ci-runtime-java-25:v3.1.0"`, allowed, sites)
	// A dotted line such as go-1.27 is one name segment, not the tag.
	collectRuntimeTagSites(".github/workflows/build-v.yml",
		`default: "ghcr.io/diggsweden/forge-ci-runtime-go-1.27:v3.1.0"`, allowed, sites)
	collectRuntimeTagSites(".github/workflows/self-runtime-container.yml",
		`local-tag: forge-ci-runtime-base:verify`, allowed, sites)
	collectRuntimeTagSites(".github/workflows/build-z.yml",
		`image: forge-ci-runtime-node-24:verify`, allowed, sites)

	collectRuntimeTagSites(".github/workflows/self-pullrequest.yml",
		`runtime-image: ghcr.io/diggsweden/forge-ci-runtime-base:v0.90.0-pre`, allowed, sites)
	collectRuntimeTagSites(".github/workflows/self-release.yml",
		`runtime-image: ghcr.io/diggsweden/forge-ci-runtime-base:${{ needs.resolve-branch.outputs.image-tag }}`, allowed, sites)
	collectRuntimeTagSites(".github/workflows/build-w.yml",
		`runtime-image: ghcr.io/diggsweden/forge-ci-runtime-base:v0.90.0-pre`, allowed, sites)

	require.Len(t, sites, 4, "expected the two versions, the non-allowlisted verify tag, and v0.90.0-pre outside the self graph")
	require.Equal(t, []string{".github/workflows/build-w.yml:1"}, sites["v0.90.0-pre"],
		"the rolling image line is only modeled for Self Pull Request")
	require.Equal(t, []string{".github/workflows/build-x.yml:1"}, sites["v3.0.0"])
	require.ElementsMatch(t, []string{".github/workflows/build-v.yml:1", ".github/workflows/build-y.yml:1"}, sites["v3.1.0"])
	require.Equal(t, []string{".github/workflows/build-z.yml:1"}, sites["verify"],
		"a :verify tag outside the allowlisted file must be flagged")
}

// binaryRefInput is the workflow input that names the forge-ci revision a
// plain-runner job installs, as opposed to the runtime images that ship it
// pre-baked.
const binaryRefInput = "forge-ci-binary-ref"

// TestReleaseRevisionIsOneModeledVersion couples the two halves of a release
// that TestRuntimeImageTagsShareOneVersion leaves independent. A job on a
// container runner gets the CLI baked into the runtime image; a job on a plain
// runner installs it from forge-ci-binary-ref. Nothing made those two agree,
// so a version cut that rewrote the image pins and missed a binary-ref default
// would run one graph on two revisions: the container jobs on the new CLI, the
// plain-runner jobs on the old one, with the mismatch showing up as behaviour
// differences between jobs rather than as an error.
//
// Digest-pinned runtime defaults are refused for the same reason. A digest is
// a stronger pin than a tag, but it carries no version, so the shared-version
// check above cannot see it and the coupling here would have nothing to compare.
// Nothing pins one today; adding one deliberately means giving it a modeled
// version here rather than letting it slip past both guards.
func TestReleaseRevisionIsOneModeledVersion(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)
	files, err := runtimetags.Files(root)
	require.NoError(t, err)

	sites := map[string][]string{}
	binaryRefs := map[string][]string{}

	var digests []string

	for _, rel := range files {
		content := reporoot.ReadFile(t, rel)
		slash := filepath.ToSlash(rel)
		collectRuntimeTagSites(slash, string(content), map[string]bool{
			".github/workflows/self-runtime-container.yml": true,
		}, sites)
		collectReleaseRevisions(t, slash, content, binaryRefs, &digests)
	}

	require.Empty(t, digests,
		"a runtime image default pinned by digest carries no version, so neither the shared-tag guard nor this one can model it")
	require.Len(t, sites, 1, "runtime image tags diverged; TestRuntimeImageTagsShareOneVersion reports the sites")
	require.NotEmpty(t, binaryRefs, "no %s defaults found — input renamed or layout changed?", binaryRefInput)

	var runtimeTag string
	for tag := range sites {
		runtimeTag = tag
	}

	// The self graph builds and consumes the CLI of the release being cut before
	// that release exists, through one rolling pre-release tag. It is a second
	// modeled revision of the same version, not a second version.
	rolling := runtimeTag + rollingPreReleaseSuffix

	// Empty defaults select one concrete stable release at runtime. The bootstrap
	// resolver returns its matching CLI ref and all runtime image references;
	// bootstrap_revision_guard_test.go executes that selection and checks wiring.
	require.Len(t, binaryRefs, 1, "binary defaults must share stable-release resolution")
	require.Contains(t, binaryRefs, "", "binary defaults must not retain an older hard-coded release")

	for site, value := range rollingPreReleaseSites(t, files) {
		require.Equalf(t, rolling, value,
			"%s pins a rolling pre-release of another version; the pre-release tracks the release being cut", site)
	}
}

// rollingPreReleaseSuffix is the tag the self graph publishes and consumes for
// the release it is currently building.
const rollingPreReleaseSuffix = "-pre"

// rollingPreReleaseSites finds every functional rolling pre-release literal in
// the runtime surface: input defaults, expression fallbacks and concurrency
// keys alike. Comment lines and input descriptions are prose, not revisions the
// run resolves, and are deliberately left alone.
func rollingPreReleaseSites(t *testing.T, files []string) map[string]string {
	t.Helper()

	found := map[string]string{}
	for _, rel := range files {
		collectRollingPreReleaseSites(filepath.ToSlash(rel), string(reporoot.ReadFile(t, rel)), found)
	}

	return found
}

func collectRollingPreReleaseSites(rel, content string, found map[string]string) {
	pattern := regexp.MustCompile(`v[0-9]+\.[0-9]+\.[0-9]+` + rollingPreReleaseSuffix)

	for index, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "description:") {
			continue
		}

		if match := pattern.FindString(line); match != "" {
			found[rel+":"+strconv.Itoa(index+1)] = match
		}
	}
}

// TestReleaseRevisionGuardCatchesDrift proves each half of the coupling fails
// when it should: a plain-runner job left on the previous CLI, a runtime image
// pinned by a digest that carries no version, and a rolling pre-release naming
// a release other than the one being cut. A guard that only ever passes against
// the current tree cannot show it would catch the next cut's missed site.
func TestReleaseRevisionGuardCatchesDrift(t *testing.T) {
	t.Parallel()

	refs, digests := map[string][]string{}, []string(nil)
	collectReleaseRevisions(t, ".github/workflows/stale.yml", []byte(`
on:
  workflow_call:
    inputs:
      forge-ci-binary-ref:
        type: string
        default: "v2.9.0"
      base-image:
        type: string
        default: "ghcr.io/diggsweden/forge-ci-runtime-base@sha256:`+strings.Repeat("a", 64)+`"
      unrelated:
        type: string
        default: "v3.0.0"
`), refs, &digests)

	require.Equal(t, map[string][]string{"v2.9.0": {".github/workflows/stale.yml:7"}}, refs,
		"only the binary-ref input names a CLI revision")
	require.Equal(t, []string{".github/workflows/stale.yml:10"}, digests,
		"a digest-pinned runtime default must be reported, not silently skipped")

	sites := map[string]string{}
	collectRollingPreReleaseSites(".github/workflows/self.yml", strings.Join([]string{
		`      version-tag: ${{ github.ref_type == 'tag' && github.ref_name || 'v2.9.0-pre' }}`,
		`      group: self-runtime-cli-v3.0.0-pre`,
		`        description: "for example v1.0.0-pre"`,
		`      # a comment mentioning v0.1.0-pre`,
		`      unrelated: "v3.0.0"`,
	}, "\n"), sites)
	require.Equal(t, map[string]string{
		".github/workflows/self.yml:1": "v2.9.0-pre",
		".github/workflows/self.yml:2": "v3.0.0-pre",
	}, sites, "expression fallbacks and concurrency keys count; prose does not")
}

// collectReleaseRevisions records every forge-ci-binary-ref default as
// "rel:line" under its value, and every digest-pinned runtime image default.
func collectReleaseRevisions(t *testing.T, rel string, content []byte, refs map[string][]string, digests *[]string) {
	t.Helper()

	// Parsed from the source rather than through effectiveWorkflow: that
	// round-trips the document, and a site is only actionable with the line the
	// author has to edit.
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil || len(doc.Content) == 0 {
		return // Not every runtime surface file is a workflow document.
	}

	inputs := mappingChild(mappingChild(mappingChild(doc.Content[0], "on"), "workflow_call"), "inputs")
	if inputs == nil || inputs.Kind != yaml.MappingNode {
		return
	}

	for index := 0; index+1 < len(inputs.Content); index += 2 {
		value := mappingChild(inputs.Content[index+1], "default")
		if value == nil || value.Kind != yaml.ScalarNode {
			continue
		}

		site := rel + ":" + strconv.Itoa(value.Line)
		if inputs.Content[index].Value == binaryRefInput {
			refs[value.Value] = append(refs[value.Value], site)

			continue
		}

		if strings.Contains(value.Value, "forge-ci-runtime") && strings.Contains(value.Value, "@sha256:") {
			*digests = append(*digests, site)
		}
	}
}
