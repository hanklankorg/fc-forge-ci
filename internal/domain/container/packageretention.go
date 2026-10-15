// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"cmp"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// PackageVersion is one version of a registry package that keeps versions
// apart from tags, as GitHub Packages does: a version is one manifest, and
// deleting it deletes every tag it holds. A registry like that cannot remove
// a single tag, so retention is decided per version.
type PackageVersion struct {
	ID      int64
	Digest  string
	Tags    []string
	Created time.Time
}

// RetentionRule names the tags a package keeps. Keep holds tags whatever their
// age, such as releases and pre-releases; of the versions with a Recent tag,
// such as a commit tag, the newest RecentCount are kept. Everything a kept
// version refers to is kept with it.
type RetentionRule struct {
	Keep        *regexp.Regexp
	Recent      *regexp.Regexp
	RecentCount int
}

// RetainedVersion is a version the plan keeps, and why.
type RetainedVersion struct {
	Version PackageVersion
	Reason  string
}

// RetentionPlan splits a package's versions into those deleted and those
// kept, each kept one with its reason.
type RetentionPlan struct {
	Delete []PackageVersion
	Keep   []RetainedVersion
}

// referrerTag is a tag that refers to another manifest by its digest: Cosign's
// signature, attestation and SBOM tags (sha256-<hex>.sig, .att, .sbom) and the
// OCI referrers tag schema's index (sha256-<hex>), the form a registry without
// the referrers API, such as GHCR, stores them in.
var referrerTag = regexp.MustCompile(`^sha256-([0-9a-f]{64})(?:\.[a-z]+)?$`)

// Validate refuses a rule that would keep nothing: such a rule deletes every
// version of the package.
func (r RetentionRule) Validate() error {
	if r.RecentCount < 0 {
		return fmt.Errorf("package retention: the number of recent versions to keep is negative (%d): %w", r.RecentCount, errs.ErrUsage)
	}

	if r.Keep == nil && (r.Recent == nil || r.RecentCount == 0) {
		return fmt.Errorf("package retention: the rule keeps no tags and would delete every version: %w", errs.ErrUsage)
	}

	if r.Recent != nil && r.RecentCount == 0 {
		return fmt.Errorf("package retention: a recent-tag pattern needs a count above zero: %w", errs.ErrUsage)
	}

	return nil
}

// RetentionRoots returns the digests the rule keeps directly, each with its
// reason: the versions holding a Keep tag, and the newest RecentCount versions
// holding a Recent tag.
func RetentionRoots(versions []PackageVersion, rule RetentionRule) (map[string]string, error) {
	if err := rule.Validate(); err != nil {
		return nil, err
	}

	roots := make(map[string]string)

	var recent []PackageVersion

	for _, version := range versions {
		if tag, ok := firstMatch(rule.Keep, version.Tags); ok {
			roots[version.Digest] = "holds kept tag " + tag

			continue
		}

		if _, ok := firstMatch(rule.Recent, version.Tags); ok {
			recent = append(recent, version)
		}
	}

	slices.SortStableFunc(recent, func(a, b PackageVersion) int { return b.Created.Compare(a.Created) })

	for _, version := range recent[:min(rule.RecentCount, len(recent))] {
		tag, _ := firstMatch(rule.Recent, version.Tags)
		roots[version.Digest] = fmt.Sprintf("holds %s, one of the %d newest recent tags", tag, rule.RecentCount)
	}

	return roots, nil
}

// RetainedClosure extends the roots to everything they keep alive: the
// manifests a kept index lists, and the signatures, attestations and referrer
// indexes that refer to a kept manifest by its digest. references holds the
// manifests each digest lists; a kept digest missing from it is returned in
// unknown, so the caller can read those manifests and call again. Once
// unknown is empty the closure is complete.
func RetainedClosure(versions []PackageVersion, roots map[string]string, references map[string][]string) (map[string]string, []string) {
	kept := make(map[string]string, len(roots))
	for digest, reason := range roots {
		kept[digest] = reason
	}

	present := make(map[string]bool, len(versions))
	for _, version := range versions {
		present[version.Digest] = true
	}

	for {
		listed := keepListed(kept, references, present)
		referred := keepReferrers(versions, kept)

		if !listed && !referred {
			break
		}
	}

	var unknown []string

	for _, digest := range sortedKeys(kept) {
		if _, known := references[digest]; !known {
			unknown = append(unknown, digest)
		}
	}

	return kept, unknown
}

// keepListed keeps every present manifest a kept index lists, and reports
// whether it kept anything new.
func keepListed(kept map[string]string, references map[string][]string, present map[string]bool) bool {
	changed := false

	for _, digest := range sortedKeys(kept) {
		for _, child := range references[digest] {
			if present[child] && kept[child] == "" {
				kept[child], changed = "listed by kept "+digest, true
			}
		}
	}

	return changed
}

// keepReferrers keeps every signature, attestation or referrer index whose
// subject is kept, and reports whether it kept anything new.
func keepReferrers(versions []PackageVersion, kept map[string]string) bool {
	changed := false

	for _, version := range versions {
		if kept[version.Digest] != "" {
			continue
		}

		if subject, ok := referredSubject(version.Tags); ok && kept[subject] != "" {
			kept[version.Digest], changed = "refers to kept "+subject, true
		}
	}

	return changed
}

// PlanRetention deletes every version the closure does not keep. A kept
// version that also holds tags the rule alone would let go keeps them: the
// registry deletes versions, not tags, so the reason says which tags stay.
func PlanRetention(versions []PackageVersion, kept map[string]string, rule RetentionRule) RetentionPlan {
	var plan RetentionPlan

	for _, version := range versions {
		reason, ok := kept[version.Digest]
		if !ok {
			plan.Delete = append(plan.Delete, version)

			continue
		}

		if extra := unkeptTags(version.Tags, rule); len(extra) != 0 && strings.HasPrefix(reason, "holds kept tag") {
			reason += "; also keeps " + strings.Join(extra, ", ") + " (the registry cannot remove one tag of a version)"
		}

		plan.Keep = append(plan.Keep, RetainedVersion{Version: version, Reason: reason})
	}

	slices.SortFunc(plan.Delete, func(a, b PackageVersion) int { return cmp.Compare(a.ID, b.ID) })
	slices.SortFunc(plan.Keep, func(a, b RetainedVersion) int { return cmp.Compare(a.Version.ID, b.Version.ID) })

	return plan
}

// ManifestReferences returns the manifests an OCI image index or Docker
// manifest list names. An image manifest names none.
func ManifestReferences(manifest []byte) ([]string, error) {
	var index struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}

	if err := json.Unmarshal(manifest, &index); err != nil {
		return nil, fmt.Errorf("package retention: parse manifest: %w: %w", err, errs.ErrMalformedInput)
	}

	references := make([]string, 0, len(index.Manifests))
	for _, entry := range index.Manifests {
		if ValidDigest(entry.Digest) {
			references = append(references, entry.Digest)
		}
	}

	return references, nil
}

func referredSubject(tags []string) (string, bool) {
	for _, tag := range tags {
		if match := referrerTag.FindStringSubmatch(tag); match != nil {
			return "sha256:" + match[1], true
		}
	}

	return "", false
}

func unkeptTags(tags []string, rule RetentionRule) []string {
	var extra []string

	for _, tag := range tags {
		if (rule.Keep == nil || !rule.Keep.MatchString(tag)) && !referrerTag.MatchString(tag) {
			extra = append(extra, tag)
		}
	}

	return extra
}

func firstMatch(pattern *regexp.Regexp, tags []string) (string, bool) {
	if pattern == nil {
		return "", false
	}

	for _, tag := range tags {
		if pattern.MatchString(tag) {
			return tag, true
		}
	}

	return "", false
}

func sortedKeys(set map[string]string) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}

	slices.Sort(keys)

	return keys
}
