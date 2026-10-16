// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// DefaultAssetRetentionKeepFiles matches the files an old release keeps: the
// checksum manifests, every signature and bundle, and the SBOMs. They are
// small, and with them a file rebuilt from the tag can still be checked
// against what was released: by its checksum, and by its own signature when
// the build is reproducible.
const DefaultAssetRetentionKeepFiles = `(?i)(checksums\.txt|sha256sums)|\.(bundle|sig|asc|pem|intoto\.jsonl?)$|\.(sbom|cyclonedx|spdx)\.json$`

// AssetRetentionInput drives `release asset-retention`.
type AssetRetentionInput struct {
	Repository string
	// Keep is how many of the newest published releases keep every file.
	Keep int
	// KeepFiles matches the files older releases keep.
	KeepFiles *regexp.Regexp
	// MaxDelete refuses a pass that would delete more files than this; 0
	// disables the bound.
	MaxDelete int
	DryRun    bool
}

// AssetRetentionResult counts what a pass deleted, or would delete.
type AssetRetentionResult struct {
	Files int
	Bytes int64
}

// ApplyAssetRetention keeps every file of the newest Keep published releases
// and, of each older release, the files KeepFiles matches; the rest are
// deleted. The releases, their notes and their tags stay. Drafts are neither
// counted nor touched, and an immutable release is counted and kept whole.
// The plan is printed and bounded before the first deletion.
func ApplyAssetRetention(ctx context.Context, pruner provider.ReleaseAssetPruner, out io.Writer, in AssetRetentionInput) (AssetRetentionResult, error) {
	if err := validateAssetRetention(pruner, in); err != nil {
		return AssetRetentionResult{}, err
	}

	repo := strings.TrimSpace(in.Repository)

	releases, err := pruner.ListReleases(ctx, repo)
	if err != nil {
		return AssetRetentionResult{}, fmt.Errorf("list the releases of %s: %w", repo, err)
	}

	plan := planAssetRetention(releases, in.Keep, in.KeepFiles)
	printAssetRetentionPlan(out, repo, in.Keep, plan, in.DryRun)

	if in.MaxDelete > 0 && plan.result.Files > in.MaxDelete {
		return AssetRetentionResult{}, fmt.Errorf("%s would lose %d release files, more than --max-delete %d: read the list, then raise the bound: %w",
			repo, plan.result.Files, in.MaxDelete, errs.ErrValidation)
	}

	if in.DryRun {
		return plan.result, nil
	}

	return deletePlannedAssets(ctx, pruner, out, repo, plan)
}

func validateAssetRetention(pruner provider.ReleaseAssetPruner, in AssetRetentionInput) error {
	switch {
	case pruner == nil:
		return fmt.Errorf("release asset pruner is required: %w", errs.ErrUsage)
	case strings.TrimSpace(in.Repository) == "":
		return fmt.Errorf("repository is required: pass --repository <owner/repo> or set $REPOSITORY: %w", errs.ErrUsage)
	case in.Keep < 1:
		return fmt.Errorf("--keep must name at least one release, not %d: %w", in.Keep, errs.ErrUsage)
	case in.KeepFiles == nil:
		return fmt.Errorf("--keep-files is required: %w", errs.ErrUsage)
	case in.MaxDelete < 0:
		return fmt.Errorf("--max-delete must not be negative: %w", errs.ErrUsage)
	}

	return nil
}

// assetRetentionPlan is the published releases newest first, and the files to
// delete from each.
type assetRetentionPlan struct {
	releases []provider.ListedRelease
	doomed   map[int][]provider.ListedAsset
	result   AssetRetentionResult
}

func planAssetRetention(listed []provider.ListedRelease, keep int, keepFiles *regexp.Regexp) assetRetentionPlan {
	releases := make([]provider.ListedRelease, 0, len(listed))

	for _, release := range listed {
		if !release.Draft {
			releases = append(releases, release)
		}
	}

	slices.SortStableFunc(releases, func(a, b provider.ListedRelease) int { return b.Created.Compare(a.Created) })

	plan := assetRetentionPlan{releases: releases, doomed: map[int][]provider.ListedAsset{}}

	for index := keep; index < len(releases); index++ {
		if releases[index].Immutable {
			continue
		}

		for _, asset := range releases[index].Assets {
			if keepFiles.MatchString(asset.Name) {
				continue
			}

			plan.doomed[index] = append(plan.doomed[index], asset)
			plan.result.Files++
			plan.result.Bytes += asset.Size
		}
	}

	return plan
}

func printAssetRetentionPlan(out io.Writer, repo string, keep int, plan assetRetentionPlan, dryRun bool) {
	verb, summary := "delete", "deleting"
	if dryRun {
		verb, summary = "would delete", "to delete"
	}

	for index, release := range plan.releases {
		if index < keep {
			_, _ = fmt.Fprintf(out, "  keep          %s (one of the %d newest releases)\n", release.Tag, keep)

			continue
		}

		if release.Immutable {
			_, _ = fmt.Fprintf(out, "  keep          %s (immutable: the forge will not change its files)\n", release.Tag)

			continue
		}

		doomed := plan.doomed[index]
		_, _ = fmt.Fprintf(out, "  trim          %s (keeps %d of %d files)\n", release.Tag, len(release.Assets)-len(doomed), len(release.Assets))

		for _, asset := range doomed {
			_, _ = fmt.Fprintf(out, "    %-12s %s (%s)\n", verb, asset.Name, megabytes(asset.Size))
		}
	}

	_, _ = fmt.Fprintf(out, "%s: %d releases, %d files (%s) %s\n", repo, len(plan.releases), plan.result.Files, megabytes(plan.result.Bytes), summary)
}

func deletePlannedAssets(ctx context.Context, pruner provider.ReleaseAssetPruner, out io.Writer, repo string, plan assetRetentionPlan) (AssetRetentionResult, error) {
	var deleted AssetRetentionResult

	for index, release := range plan.releases {
		for _, asset := range plan.doomed[index] {
			if err := pruner.DeleteReleaseAsset(ctx, repo, release, asset); err != nil {
				return deleted, fmt.Errorf("delete %s from release %s: %w", asset.Name, release.Tag, err)
			}

			deleted.Files++
			deleted.Bytes += asset.Size
		}
	}

	_, _ = fmt.Fprintf(out, "%s: deleted %d files (%s)\n", repo, deleted.Files, megabytes(deleted.Bytes))

	return deleted, nil
}

// megabytes renders a size for the plan; GitLab reports none.
func megabytes(size int64) string {
	if size == 0 {
		return "size not reported"
	}

	return fmt.Sprintf("%.1f MB", float64(size)/1e6)
}
