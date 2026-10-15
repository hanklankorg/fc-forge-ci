// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"fmt"
	"io"
	"strings"

	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// packageVersionStore lists and deletes the versions of one package of a
// registry that keeps versions apart from tags, such as GitHub Packages.
type packageVersionStore interface {
	PackageVersions(ctx context.Context, owner, name string) ([]domaincontainer.PackageVersion, error)
	DeletePackageVersion(ctx context.Context, owner, name string, id int64) error
}

// maxClosureRounds bounds how deep the closure follows indexes: an index of
// referrers of a platform manifest of an index is three; anything deeper is
// not an image this engine published.
const maxClosureRounds = 8

// PackageRetentionInput drives ApplyPackageRetention.
type PackageRetentionInput struct {
	// Repository is the package, as host/owner/name.
	Repository string
	Rule       domaincontainer.RetentionRule
	// MaxDelete bounds the versions one pass deletes; zero disables it.
	MaxDelete int
	DryRun    bool
}

// PackageRetentionResult reports what a pass planned and did.
type PackageRetentionResult struct {
	Versions int
	Kept     int
	Planned  int
	Deleted  []int64
}

// ApplyPackageRetention deletes the versions of one package the rule does not
// keep. A version is kept when it holds a kept tag, is among the newest recent
// ones, or is listed by or refers to a kept version: deleting a kept index's
// platform manifest or signature would break the image left behind. A
// manifest that cannot be read stops the pass rather than guessing what it
// lists.
func ApplyPackageRetention(ctx context.Context, store packageVersionStore, manifests RawManifestRegistry, out io.Writer, in PackageRetentionInput) (PackageRetentionResult, error) {
	owner, name, err := splitPackageRepository(in.Repository)
	if err != nil {
		return PackageRetentionResult{}, err
	}

	versions, err := store.PackageVersions(ctx, owner, name)
	if err != nil {
		return PackageRetentionResult{}, err
	}

	result := PackageRetentionResult{Versions: len(versions)}

	plan, err := planPackageRetention(ctx, manifests, in, versions)
	if err != nil {
		return result, err
	}

	result.Kept, result.Planned = len(plan.Keep), len(plan.Delete)

	if in.MaxDelete > 0 && len(plan.Delete) > in.MaxDelete {
		return result, fmt.Errorf("package retention: %s would lose %d versions, more than --max-delete %d; check the rule, then raise the bound: %w",
			in.Repository, len(plan.Delete), in.MaxDelete, errs.ErrValidation)
	}

	reportRetentionPlan(out, in.Repository, plan, in.DryRun)

	if in.DryRun {
		return result, nil
	}

	for _, version := range plan.Delete {
		if err := store.DeletePackageVersion(ctx, owner, name, version.ID); err != nil {
			return result, fmt.Errorf("package retention: %s: %w", in.Repository, err)
		}

		result.Deleted = append(result.Deleted, version.ID)
	}

	_, _ = fmt.Fprintf(out, "Deleted %d versions of %s; %d kept.\n", len(result.Deleted), in.Repository, result.Kept)

	return result, nil
}

// planPackageRetention decides what to keep and delete, refusing a rule that
// keeps none of the versions.
func planPackageRetention(ctx context.Context, manifests RawManifestRegistry, in PackageRetentionInput, versions []domaincontainer.PackageVersion) (domaincontainer.RetentionPlan, error) {
	roots, err := domaincontainer.RetentionRoots(versions, in.Rule)
	if err != nil {
		return domaincontainer.RetentionPlan{}, err
	}

	if len(roots) == 0 && len(versions) != 0 {
		return domaincontainer.RetentionPlan{}, fmt.Errorf("package retention: the rule keeps none of the %d versions of %s; refusing to delete them all: %w",
			len(versions), in.Repository, errs.ErrValidation)
	}

	kept, err := retainedClosure(ctx, manifests, in.Repository, versions, roots)
	if err != nil {
		return domaincontainer.RetentionPlan{}, err
	}

	return domaincontainer.PlanRetention(versions, kept, in.Rule), nil
}

// retainedClosure reads the manifests of kept versions until the domain knows
// everything they list.
func retainedClosure(ctx context.Context, manifests RawManifestRegistry, repository string, versions []domaincontainer.PackageVersion, roots map[string]string) (map[string]string, error) {
	references := make(map[string][]string)

	for range maxClosureRounds {
		kept, unknown := domaincontainer.RetainedClosure(versions, roots, references)
		if len(unknown) == 0 {
			return kept, nil
		}

		for _, digest := range unknown {
			manifest, err := manifests.Manifest(ctx, repository+"@"+digest)
			if err != nil {
				return nil, fmt.Errorf("package retention: read kept manifest %s@%s: %w", repository, digest, err)
			}

			listed, err := domaincontainer.ManifestReferences(manifest)
			if err != nil {
				return nil, fmt.Errorf("package retention: %s@%s: %w", repository, digest, err)
			}

			references[digest] = listed
		}
	}

	return nil, fmt.Errorf("package retention: %s nests indexes deeper than %d levels: %w", repository, maxClosureRounds, errs.ErrValidation)
}

func reportRetentionPlan(out io.Writer, repository string, plan domaincontainer.RetentionPlan, dryRun bool) {
	verb := "delete"
	if dryRun {
		verb = "would delete"
	}

	_, _ = fmt.Fprintf(out, "%s: %d versions kept, %d to delete\n", repository, len(plan.Keep), len(plan.Delete))

	for _, kept := range plan.Keep {
		_, _ = fmt.Fprintf(out, "  keep          %s %s (%s)\n", kept.Version.Digest, tagList(kept.Version.Tags), kept.Reason)
	}

	for _, version := range plan.Delete {
		_, _ = fmt.Fprintf(out, "  %-13s %s %s\n", verb, version.Digest, tagList(version.Tags))
	}
}

func tagList(tags []string) string {
	if len(tags) == 0 {
		return "(untagged)"
	}

	return strings.Join(tags, ",")
}

// splitPackageRepository splits host/owner/name; the name may hold slashes.
func splitPackageRepository(repository string) (string, string, error) {
	_, path, ok := strings.Cut(strings.TrimSpace(repository), "/")
	owner, name, nameOK := strings.Cut(path, "/")

	if !ok || !nameOK || owner == "" || name == "" || strings.ContainsAny(repository, ":@") {
		return "", "", fmt.Errorf("package retention: %q is not host/owner/name: %w", repository, errs.ErrUsage)
	}

	return owner, name, nil
}
