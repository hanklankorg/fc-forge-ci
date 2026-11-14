// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package summary

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/ci"
	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/git"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	domainsummary "github.com/diggsweden/forge-ci/internal/domain/summary"
	"github.com/diggsweden/forge-ci/internal/domain/validate"
	"github.com/diggsweden/forge-ci/internal/listval"
)

// gitInfoOps is the slice of git.Repo methods the prerequisites summary
// needs. Tests inject a fake; production passes adapter/git.New(). The
// CommitInfo return type lives in domain/git so this file stays
// adapter-free.
type gitInfoOps interface {
	TaggerInfo(ctx context.Context, tag string) (git.TaggerInfo, error)
	TagMessage(ctx context.Context, tag string) (string, error)
	CatFileTag(ctx context.Context, tag string) (string, error)
	CommitInfo(ctx context.Context, sha string) (git.CommitInfo, error)
}

// PrerequisitesSummaryInput drives `summary prerequisites`. The use case
// composes ~6 markdown sections (tag info / commit info / configuration
// / secrets status / job status / validation results / footer).
type PrerequisitesSummaryInput struct {
	TagName                  string
	CommitSHA                string
	RefType                  provider.RefType
	ConfigPlanJSON           string
	ProjectTypes             string // optional pre-resolved comma list
	BuildTypes               string // optional pre-resolved comma list
	ContainerRegistry        string
	SignArtifacts            bool
	RequireAllowlistedSigner bool
	JobStatus                domainsummary.Result
	PublishTo                string // "maven-central,npmjs,forge-packages" CSV
	// Forge selects the guide a failed job points at; local or unknown
	// forges get no link.
	Forge provider.ForgeAPI

	// Boolean secret presence flags
	HasReleaseGPGPrivateKey bool
	HasReleaseGPGPassphrase bool
	HasReleaseToken         bool
	HasReleaseGPGPublicKey  bool
	HasMavenCentralUsername bool
	HasMavenCentralPassword bool
	HasNPMToken             bool

	Now time.Time
}

// Prerequisites appends the prerequisites validation report to the
// step summary.
func Prerequisites(ctx context.Context, sink ci.SummarySink, gitr gitInfoOps, in PrerequisitesSummaryInput) error {
	if err := enrichPrerequisitesFromConfigPlan(&in); err != nil {
		return err
	}

	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}

	var builder strings.Builder
	if err := writeTagInfo(ctx, &builder, gitr, in); err != nil {
		return err
	}

	if err := writeCommitInfo(ctx, &builder, gitr, in); err != nil {
		return err
	}

	writeConfiguration(&builder, in)
	writeSecretsStatus(&builder, in)
	writeJobStatus(&builder, in)
	writeValidationResults(&builder, in)
	_, _ = fmt.Fprintf(&builder, "\n---\n*Generated at: %s*\n", now.UTC().Format("2006-01-02 15:04:05 UTC"))

	return sink.Append(ctx, builder.String())
}

func enrichPrerequisitesFromConfigPlan(in *PrerequisitesSummaryInput) error {
	if strings.TrimSpace(in.ConfigPlanJSON) == "" {
		return nil
	}

	plan, err := pipeline.DecodeConfigPlan(in.ConfigPlanJSON)
	if err != nil {
		return err
	}

	if plan.Version != pipeline.ConfigPlanVersion {
		return fmt.Errorf("config-plan-json has unsupported version %d: %w", plan.Version, errs.ErrInvalidConfig)
	}

	if err := pipeline.ValidateConfigPlan(plan); err != nil {
		return err
	}

	projectTypes := map[string]bool{}
	buildTypes := map[string]bool{}
	publishTo := map[string]bool{}

	for _, artifact := range plan.Artifacts.All {
		if artifact.ProjectType != "" {
			projectTypes[string(artifact.ProjectType)] = true
		}

		if artifact.BuildType != "" {
			buildTypes[string(artifact.BuildType)] = true
		}

		for _, target := range artifact.PublishTo {
			if target != "" {
				publishTo[string(target)] = true
			}
		}
	}

	if in.ProjectTypes == "" {
		in.ProjectTypes = sortedCSV(projectTypes)
	}

	if in.BuildTypes == "" {
		in.BuildTypes = sortedCSV(buildTypes)
	}

	if in.PublishTo == "" {
		in.PublishTo = sortedCSV(publishTo)
	}

	return nil
}

func sortedCSV(values map[string]bool) string {
	if len(values) == 0 {
		return ""
	}

	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}

	slices.Sort(out)

	return strings.Join(out, ",")
}

func writeTagInfo(ctx context.Context, builder *strings.Builder, gitr gitInfoOps, in PrerequisitesSummaryInput) error {
	_, _ = fmt.Fprintf(builder, "# 📋 Release Prerequisites Validation Report\n\n")
	_, _ = fmt.Fprintf(builder, "## 🏷️ Release Tag\n")
	_, _ = fmt.Fprintf(builder, "- **Tag:** %s\n", domainsummary.InlineCode(in.TagName))
	_, _ = fmt.Fprintf(builder, "- **Type:** %s\n", in.RefType)

	if in.RefType != provider.RefTypeTag || gitr == nil {
		return nil
	}

	info, err := gitr.TaggerInfo(ctx, in.TagName)
	tagger, date := info.Tagger, info.Date

	if err != nil {
		tagger, date = "N/A", "N/A" //nolint:goconst // test fixture / generic identifier — extracting would explode setup boilerplate.
	}

	if tagger == "" {
		tagger = "N/A"
	}

	if date == "" {
		date = "N/A"
	}

	msg, err := gitr.TagMessage(ctx, in.TagName)
	if err != nil || msg == "" {
		msg = "No message"
	} else {
		// First line only.
		msg, _, _ = strings.Cut(msg, "\n")
	}
	// Signature presence comes from the raw tag body — strictly
	// faster than the deleted `git tag -v` subprocess, and the summary
	// only needs "is a signature there?" not "does it verify against
	// some keyring."
	sig := "Unavailable"

	if body, err := gitr.CatFileTag(ctx, in.TagName); err == nil {
		sig = "Not signed"

		switch {
		case strings.Contains(body, "BEGIN PGP SIGNATURE"):
			sig = "GPG signed"
		case strings.Contains(body, "BEGIN SSH SIGNATURE"):
			sig = "SSH signed"
		}
	}

	// Tag and commit metadata is written by whoever created the tag or commit,
	// so it is rendered as literal text rather than Markdown.
	_, _ = fmt.Fprintf(builder, "- **Tagger:** %s\n", domainsummary.LiteralText(tagger))
	_, _ = fmt.Fprintf(builder, "- **Tag Date:** %s\n", domainsummary.LiteralText(date))
	_, _ = fmt.Fprintf(builder, "- **Tag Signature:** %s\n", sig)
	_, _ = fmt.Fprintf(builder, "- **Tag Message:** %s\n", domainsummary.LiteralText(msg))

	return nil
}

func writeCommitInfo(ctx context.Context, builder *strings.Builder, gitr gitInfoOps, in PrerequisitesSummaryInput) error {
	if gitr == nil || in.CommitSHA == "" {
		return nil
	}

	info, err := gitr.CommitInfo(ctx, in.CommitSHA)
	if err != nil {
		// Best-effort: skip the commit-info section, but log so the
		// blank section in the summary correlates with a real cause.
		slog.Warn("summary prerequisites: skipping commit section",
			"sha", in.CommitSHA, "err", err)

		return nil
	}

	sig := "Not signed"
	if strings.Contains(info.Body, "BEGIN PGP SIGNATURE") {
		sig = "GPG signed"
	} else if strings.Contains(info.Body, "BEGIN SSH SIGNATURE") {
		sig = "SSH signed"
	}

	_, _ = fmt.Fprintf(builder, "\n## 📦 Tagged Commit\n")
	_, _ = fmt.Fprintf(builder, "- **SHA:** %s\n", domainsummary.InlineCode(in.CommitSHA))
	_, _ = fmt.Fprintf(builder, "- **Author:** %s\n", domainsummary.LiteralText(info.Author))
	_, _ = fmt.Fprintf(builder, "- **Date:** %s\n", domainsummary.LiteralText(info.Date))
	_, _ = fmt.Fprintf(builder, "- **Signature:** %s\n", sig)
	_, _ = fmt.Fprintf(builder, "- **Message:** %s\n", domainsummary.LiteralText(info.Message))

	return nil
}

func writeConfiguration(builder *strings.Builder, in PrerequisitesSummaryInput) {
	signing := "Disabled"
	if in.SignArtifacts {
		signing = "Enabled"
	}

	projectTypes := in.ProjectTypes
	if projectTypes == "" {
		projectTypes = "(unspecified)"
	}

	buildTypes := in.BuildTypes
	if buildTypes == "" {
		buildTypes = "(unspecified)"
	}

	_, _ = fmt.Fprintf(builder, "\n## ⚙️ Configuration\n")
	_, _ = fmt.Fprintf(builder, "| Setting | Value |\n")
	_, _ = fmt.Fprintf(builder, "|---------|-------|\n")
	_, _ = fmt.Fprintf(builder, "| **Project Types** | %s |\n", domainsummary.SanitizeCell(projectTypes))
	_, _ = fmt.Fprintf(builder, "| **Build Types** | %s |\n", domainsummary.SanitizeCell(buildTypes))
	_, _ = fmt.Fprintf(builder, "| **Container Registry** | %s |\n", domainsummary.SanitizeCell(in.ContainerRegistry))
	_, _ = fmt.Fprintf(builder, "| **Release Publisher** | GitHub CLI |\n")
	_, _ = fmt.Fprintf(builder, "| **GPG Signing** | %s |\n", signing)
}

func writeSecretsStatus(builder *strings.Builder, in PrerequisitesSummaryInput) {
	_, _ = fmt.Fprintf(builder, "\n## 🔑 Required Secrets Status\n\n")
	_, _ = fmt.Fprintf(builder, "| Secret | Purpose | Status |\n")
	_, _ = fmt.Fprintf(builder, "|--------|---------|--------|\n")

	row := func(name, purpose string, has bool) {
		status := "✗ Missing"
		if has {
			status = "✓ Available"
		}

		_, _ = fmt.Fprintf(builder, "| %s | %s | %s |\n", name, purpose, status)
	}
	if in.SignArtifacts {
		row("RELEASE_GPG_PRIVATE_KEY", "Sign commits/artifacts", in.HasReleaseGPGPrivateKey)
		row("RELEASE_GPG_PASSPHRASE", "GPG passphrase", in.HasReleaseGPGPassphrase)
	}

	row("RELEASE_TOKEN", "Push commits & releases", in.HasReleaseToken)

	if in.SignArtifacts {
		row("RELEASE_GPG_PUBLIC_KEY", "GPG verification", in.HasReleaseGPGPublicKey)
	}

	if in.PublishTo != "" {
		targets := parsePublishTargets(in.PublishTo)
		if slices.Contains(targets, config.PublishMavenCentral) {
			row("MAVEN_CENTRAL_USERNAME", "Maven Central auth", in.HasMavenCentralUsername)
			row("MAVEN_CENTRAL_PASSWORD", "Maven Central auth", in.HasMavenCentralPassword)
		}

		if slices.Contains(targets, config.PublishNPMJS) {
			row("NPM_TOKEN", "NPM registry auth", in.HasNPMToken)
		}
	}
}

func writeJobStatus(builder *strings.Builder, in PrerequisitesSummaryInput) {
	_, _ = fmt.Fprintf(builder, "\n")

	if in.JobStatus == domainsummary.ResultSuccess {
		_, _ = fmt.Fprintf(builder, "### ✓ All selected prerequisite checks passed!\n")
		_, _ = fmt.Fprintf(builder, "Release preflight completed.\n")
	} else {
		_, _ = fmt.Fprintf(builder, "### ✗ Prerequisites validation failed\n")

		if guide := in.Forge.TroubleshootingGuide(); guide != "" {
			_, _ = fmt.Fprintf(builder, "Each symptom and its fix is in the guide's Troubleshooting table:\n%s\n", guide)
		} else {
			_, _ = fmt.Fprintf(builder, "Review the failed checks before attempting release.\n")
		}
	}
}

func writeValidationResults(builder *strings.Builder, in PrerequisitesSummaryInput) {
	_, _ = fmt.Fprintf(builder, "\n## ✓ Validation Results\n\n")
	_, _ = fmt.Fprintf(builder, "| Validation | Result | Details |\n")
	_, _ = fmt.Fprintf(builder, "|------------|--------|---------|\n")

	row := func(name, result, details string) {
		_, _ = fmt.Fprintf(builder, "| %s | %s | %s |\n", name, result, details)
	}

	if in.RefType == provider.RefTypeTag {
		tag, err := validate.ParseTagFormat(in.TagName)
		if err == nil {
			row("Semantic Version", "✓ Pass", fmt.Sprintf("`%s` follows vX.Y.Z", in.TagName))
		} else {
			row("Semantic Version", "✗ Fail", "Invalid format")
		}

		row("Tag Type", "Unconfirmed", "Object type is not verified by this summary")
		row("Tag Signature", "Unconfirmed", "Signature presence is not verification; see the prerequisite job result")

		if tag != nil && tag.Prerelease != "" {
			identifier, _, _ := strings.Cut(tag.Prerelease, ".")
			row("Release Type", "🚧 Pre-release", fmt.Sprintf("`%s` version", identifier))
		} else {
			row("Release Type", "🎯 Stable", "Production release")
		}
	}

	switch {
	case validate.IsSnapshot(in.TagName):
		row("Signer Allowlist", "− Skip", "SNAPSHOT release (bypasses gate)")
	case in.RequireAllowlistedSigner:
		row("Signer Allowlist", "✓ Enforced", ".forge-ci/allowed_{signers,gpg_fingerprints}")
	}

	if in.HasReleaseToken {
		row("Release Token", "✓ Pass", "Valid GitHub token")
	} else {
		row("Release Token", "✗ Fail", "Missing RELEASE_TOKEN")
	}

	emitPublishRows(in, row)
}

// emitPublishRows renders one summary row per resolved publish target.
// Split out from the parent table builder so nestif doesn't trip on
// the per-target if/else nesting.
func emitPublishRows(in PrerequisitesSummaryInput, row func(name, status, detail string)) {
	if in.PublishTo == "" {
		return
	}

	targets := parsePublishTargets(in.PublishTo)

	if slices.Contains(targets, config.PublishMavenCentral) {
		if in.HasMavenCentralUsername && in.HasMavenCentralPassword {
			row("Maven Central", "✓ Pass", "Credentials configured")
		} else {
			row("Maven Central", "✗ Fail", "Missing credentials")
		}
	}

	if slices.Contains(targets, config.PublishNPMJS) {
		if in.HasNPMToken {
			row("NPM Registry", "✓ Pass", "Token configured")
		} else {
			row("NPM Registry", "✗ Fail", "Missing NPM_TOKEN")
		}
	}

	if slices.Contains(targets, config.PublishForgePackages) {
		// Forge-neutral target: the package registry is GitHub Packages,
		// GitLab Package Registry or Forgejo packages depending on the runner,
		// each authenticated with the forge-injected token. Label it neutrally
		// so the summary is truthful on every forge, not just GitHub.
		row("Forge Packages", "✓ Pass", "Using forge token")
	}
}

// parsePublishTargets splits the PublishTo CSV and returns a sorted
// slice of typed config.PublishTarget values. Empty entries are
// dropped. Unknown tokens flow through as untyped enum values —
// validation that they're in config.ValidPublishTargets is the
// config-parse step's job; this function is consumed only after that.
func parsePublishTargets(s string) []config.PublishTarget {
	parts := listval.Tokens(s)

	out := make([]config.PublishTarget, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, config.PublishTarget(p))
		}
	}

	slices.Sort(out)

	return out
}
