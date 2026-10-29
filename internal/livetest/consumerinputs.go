// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// Migrate only the release signer's former destination input in the scratch
// workflow. The current input binds the same ledger allowlist for signing,
// promotion and cleanup. Keep the value (including its variable/default), all
// trust inputs and secrets verbatim; never translate an independent authority.
func migratePublicationRepository(text string) (string, error) {
	var document struct {
		Jobs map[string]struct {
			Uses string               `yaml:"uses"`
			With map[string]yaml.Node `yaml:"with"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(text), &document); err != nil {
		return "", fmt.Errorf("parse consumer workflow: %w", err)
	}
	lines := strings.Split(text, "\n")
	for name, job := range document.Jobs {
		if !strings.Contains(job.Uses, "/.forgejo/workflows/sign-and-publish-release.yml@") || strings.HasPrefix(job.Uses, "./") {
			continue
		}
		old, found := job.With["publication-image-repository"]
		if !found {
			continue
		}
		if _, both := job.With["image-repository"]; both {
			return "", fmt.Errorf("job %q passes both publication-image-repository and image-repository; independent publication authority cannot be translated: %w", name, errs.ErrValidation)
		}
		// The value node's line identifies the original field without reserializing
		// YAML or changing expressions, comments, keys, or unrelated workflow calls.
		line := old.Line - 1
		if line < 0 || line >= len(lines) || !strings.HasPrefix(strings.TrimSpace(lines[line]), "publication-image-repository:") {
			return "", fmt.Errorf("job %q has an unsupported publication destination spelling: %w", name, errs.ErrValidation)
		}
		lines[line] = strings.Replace(lines[line], "publication-image-repository:", "image-repository:", 1)
	}
	return strings.Join(lines, "\n"), nil
}
