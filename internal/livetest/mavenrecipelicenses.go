// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// These are the entire app/library recipe overlay, not a consumer allowlist.
// Keep the provenance alongside every copied file, including configurations
// whose inline headers currently duplicate it. No consumer REUSE policy changes.
var mavenConsumerRecipeFiles = []string{
	".forgejo/workflows/pullrequest.yml",
	".forgejo/workflows/release.yml",
	".chglog/config-keepachangelog.yml",
	".chglog/config-minimal.yml",
	".chglog/CHANGELOG-keepachangelog.tpl",
	".chglog/CHANGELOG-minimal.tpl",
}

// The engine REUSE.toml's test/live/demos/** override owns these recipe files.
// Split the tags so scanners do not attribute the recipe's license to this Go file.
const mavenRecipeLicenseMetadata = "SPDX-FileCopyrightText" + ": 2026 Digg - Agency for Digital Government\n" +
	"SPDX-License-Identifier" + ": CC0-1.0\n"

func stageMavenRecipeLicenses(ctx context.Context, source Consumer, recipe string) error {
	for _, file := range mavenConsumerRecipeFiles {
		if err := source.stageFile(ctx, "100644", file+".license", mavenRecipeLicenseMetadata); err != nil {
			return err
		}
	}
	// Preserve existing consumer license texts, including LICENSE itself. Check
	// the private index, never the mutable source worktree. Only supply CC0 when
	// absent, using the exact upstream text from the documented recipe checkout.
	const license = "LICENSES/CC0-1.0.txt"
	entry, err := source.git.run(ctx, source.stage, "ls-files", "--stage", "--", license)
	if err != nil {
		return err
	}
	if entry != "" {
		return nil
	}
	body, err := os.ReadFile(filepath.Join(recipe, "..", "..", "..", "..", "..", license))
	if err != nil {
		return fmt.Errorf("maven recipe license text: %w", err)
	}
	return source.stageFile(ctx, "100644", license, string(body))
}
