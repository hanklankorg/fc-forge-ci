// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// ForgejoReleaseOptionVariables are the repository variables a Forgejo
// trial gives a release option as, and a consumer's release workflow reads
// into the layer's input of the same meaning: release-draft, release-policy
// and release-asset-retention. None without an option. Retention keeps the
// newest release whole, so the first of two is trimmed.
func ForgejoReleaseOptionVariables(option string) map[string]string {
	switch option {
	case OptionDraft:
		return map[string]string{"RELEASE_DRAFT": variableTrue}
	case OptionPrerelease:
		return map[string]string{"RELEASE_POLICY": "stable-prerelease"}
	case OptionRetention:
		return map[string]string{"RELEASE_ASSET_RETENTION": "1"}
	default:
		return nil
	}
}

// GitLabReleaseOptionInputs are the release component's inputs a GitLab
// trial gives a release option as: none without one, and none for a draft,
// which GitLab does not have.
func GitLabReleaseOptionInputs(option string) map[string]any {
	switch option {
	case OptionPrerelease:
		return map[string]any{"release-policy": "stable-prerelease"}
	case OptionRetention:
		return map[string]any{"release-asset-retention": "1"}
	default:
		return nil
	}
}

// ReleaseOptionFindings holds the state a forge recorded of a release to the
// trial's release option: a draft with OptionDraft and published with any
// other, a prerelease with OptionPrerelease and none with any other. Without
// an option the state is the consumer's choice and nothing is judged.
func ReleaseOptionFindings(option string, draft, prerelease bool) []string {
	if option == "" {
		return nil
	}

	var findings []string

	if want := option == OptionDraft; draft != want {
		findings = append(findings, fmt.Sprintf("with release option %s the release is draft=%t, want %t", option, draft, want))
	}

	if want := option == OptionPrerelease; prerelease != want {
		findings = append(findings, fmt.Sprintf("with release option %s the release is prerelease=%t, want %t", option, prerelease, want))
	}

	return findings
}

// BaseStageReuseFindings holds a later release of an unchanged base graph to
// reuse of every stage an earlier release promoted, as GitLab's release
// promotes a stage (.gitlab/bin/release.sh, rc_base_promote): the trace of
// each stage's promotion job, traces by stage name, says it found every
// final tag of the stage and verified its signature, SBOM and lineage
// against the pinned key and the declared inputs, and so signed and
// promoted nothing; and the later release hands on exactly the verified
// projections the earlier did, each flavor's digest and input ID (first,
// later).
func BaseStageReuseFindings(stages []string, traces map[string]string, first, later []string) []string {
	var findings []string

	for _, stage := range stages {
		if reused := "base stage " + stage + ": already promoted and verified; reused."; !strings.Contains(traces[stage], reused) {
			findings = append(findings, fmt.Sprintf("the later release did not reuse the base stage %s, verified: its promotion never said %q", stage, reused))
		}
	}

	if len(stages) == 0 || len(first) == 0 {
		findings = append(findings, fmt.Sprintf("the earlier release promoted the base images %v of the stages %v, and there is nothing to reuse", first, stages))
	}

	if !slices.Equal(slices.Sorted(slices.Values(first)), slices.Sorted(slices.Values(later))) {
		findings = append(findings, fmt.Sprintf("the later release handed on the base images %v, the earlier %v", later, first))
	}

	return findings
}

// retainedReleaseFile matches what retention says an older release keeps:
// its checksums, its signatures and provenance, its SBOMs, the native
// sbom-<image>-<arch>.json inventories among them, and release-plan.json.
// It is written from what the release-asset-retention input and
// `release asset-retention` promise, not copied from the product's pattern,
// which it is here to judge.
var retainedReleaseFile = regexp.MustCompile(`(?i)checksums|sha256sums|\.(bundle|sig|asc|pem)$|\.intoto\.jsonl?$|\.(cdx|cyclonedx|spdx|sbom)\.json$|^sbom-.+\.json$|^release-plan\.json$`)

// ReleaseRetentionFindings holds an older release to retention of one
// release: before and after are its files before and after the newer release
// was published, newest the newer release's. After, the older release offers
// exactly the files of before that retention keeps: none of those is gone,
// every other file is, and nothing was added. It offered at least one file
// retention removes, or the case shows nothing, and the newer release still
// offers one.
func ReleaseRetentionFindings(before, after, newest []string) []string {
	var findings []string

	removable := false

	for _, name := range before {
		kept, offered := retainedReleaseFile.MatchString(name), slices.Contains(after, name)
		removable = removable || !kept

		switch {
		case kept && !offered:
			findings = append(findings, "retention removed "+name+", which an older release keeps")
		case !kept && offered:
			findings = append(findings, "retention left "+name+", which an older release loses")
		}
	}

	for _, name := range after {
		if !slices.Contains(before, name) {
			findings = append(findings, "the older release gained "+name)
		}
	}

	if !removable {
		findings = append(findings, fmt.Sprintf("the older release offered no file retention removes: %v", before))
	}

	if !slices.ContainsFunc(newest, func(name string) bool { return !retainedReleaseFile.MatchString(name) }) {
		findings = append(findings, fmt.Sprintf("the newest release offers no file beside those an older release keeps: %v", newest))
	}

	return findings
}
