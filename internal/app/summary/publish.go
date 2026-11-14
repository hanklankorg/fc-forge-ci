// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package summary

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/ci"
	domainsummary "github.com/diggsweden/forge-ci/internal/domain/summary"
)

// MavenCentralPublishInput drives MavenCentralPublish.
type MavenCentralPublishInput struct {
	Version    string
	IsSnapshot bool
	Now        time.Time
}

// MavenCentralPublish appends the Maven Central publish summary block.
func MavenCentralPublish(ctx context.Context, sink ci.SummarySink, in MavenCentralPublishInput) error {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}

	var builder strings.Builder

	_, _ = fmt.Fprintf(&builder, "## Maven Central deployment command completed\n\n")
	_, _ = fmt.Fprintf(&builder, "- **Version:** %s\n", domainsummary.LiteralText(in.Version))

	if in.IsSnapshot {
		_, _ = fmt.Fprintf(&builder, "- **Type:** SNAPSHOT\n")
	} else {
		_, _ = fmt.Fprintf(&builder, "- **Type:** Release\n")
	}

	if in.IsSnapshot {
		_, _ = fmt.Fprintf(&builder, "- **Registry:** [Central snapshots](https://central.sonatype.com/repository/maven-snapshots/)\n\n")
		_, _ = fmt.Fprintf(&builder, "The project's Maven profile reported completion. Snapshot availability was not independently observed; verify the snapshot repository before consuming this version.\n")
	} else {
		_, _ = fmt.Fprintf(&builder, "- **Registry:** [Maven Central](https://central.sonatype.com/)\n\n")
		_, _ = fmt.Fprintf(&builder, "The project's Maven profile reported completion. Publication state was not independently observed; check the Central Portal before treating this version as publicly available.\n")
	}

	_, _ = fmt.Fprintf(&builder, "\n*Command completed at %s*\n", now.UTC().Format("2006-01-02 15:04:05 UTC"))

	return sink.Append(ctx, builder.String())
}

// ForgePackagesPublishInput drives ForgePackagesPublish.
type ForgePackagesPublishInput struct {
	Repository  string
	PackageType string
	// RegistryName is the forge-native registry's display name (e.g.
	// "GitHub Packages", "GitLab Package Registry"). Empty falls back to a
	// forge-neutral label — the publish target is the forge's own registry,
	// resolved per forge, so this block must not hard-code GitHub.
	RegistryName string
	Now          time.Time
}

// ForgePackagesPublish appends the forge-native package-registry publish
// summary block (GitHub Packages / GitLab Package Registry / Forgejo packages).
func ForgePackagesPublish(ctx context.Context, sink ci.SummarySink, in ForgePackagesPublishInput) error {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}

	registry := domainsummary.LiteralText(in.RegistryName)
	if registry == "" {
		registry = "the forge package registry"
	}

	var builder strings.Builder

	_, _ = fmt.Fprintf(&builder, "## Published to %s 📦\n\n", registry)
	_, _ = fmt.Fprintf(&builder, "- **Package Type:** %s\n", domainsummary.LiteralText(in.PackageType))
	_, _ = fmt.Fprintf(&builder, "- **Registry:** %s\n", registry)
	_, _ = fmt.Fprintf(&builder, "- **Repository:** %s\n\n", domainsummary.LiteralText(in.Repository))
	_, _ = fmt.Fprintf(&builder, "*Published at %s*\n", now.UTC().Format("2006-01-02 15:04:05 UTC"))

	return sink.Append(ctx, builder.String())
}
