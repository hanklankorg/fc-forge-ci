// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// NPMPackedEntrypoint resolves only the supported literal packed-metadata
// contract. It neither reads files nor executes package code. A nil body means
// absent metadata; CLI packages retain the historical dist/cli.js requirement.
func NPMPackedEntrypoint(body []byte) (string, error) {
	const cliDefault = "dist/cli.js"
	if body == nil {
		return cliDefault, nil
	}

	var meta struct {
		Bin     json.RawMessage `json:"bin"`
		Exports json.RawMessage `json:"exports"`
		Main    string          `json:"main"`
	}
	if err := json.Unmarshal(body, &meta); err != nil {
		return "", fmt.Errorf("parse packed package metadata: %w", errs.ErrValidation)
	}

	if len(meta.Bin) != 0 && string(meta.Bin) != "null" {
		return cliDefault, nil
	}

	entrypoint := meta.Main
	if len(meta.Exports) != 0 && string(meta.Exports) != "null" {
		exported, err := npmExportsEntrypoint(meta.Exports)
		if err != nil {
			return "", err
		}

		entrypoint = exported
	}

	if entrypoint == "" {
		return cliDefault, nil
	}

	if !pathsafe.Relative(entrypoint) || filepath.Clean(entrypoint) == "." || strings.ContainsFunc(entrypoint, unicode.IsControl) || strings.HasSuffix(entrypoint, " ") {
		return "", fmt.Errorf("packed package entrypoint must be a relative file: %w", errs.ErrValidation)
	}

	return entrypoint, nil
}

// npmExportsEntrypoint accepts only an exports value that is one literal ./
// file target Node resolves to the same filesystem path.
func npmExportsEntrypoint(exports json.RawMessage) (string, error) {
	var entrypoint string
	if err := json.Unmarshal(exports, &entrypoint); err != nil || !strings.HasPrefix(entrypoint, "./") || strings.ContainsAny(entrypoint, `\%?#`) {
		return "", fmt.Errorf("packed library exports must name a literal ./ file target: %w", errs.ErrValidation)
	}
	// Node resolves exports as URLs. Trailing ASCII space/control bytes
	// would be stripped, making Node load a different filesystem path.
	if strings.ContainsFunc(entrypoint, unicode.IsControl) || strings.HasSuffix(entrypoint, " ") {
		return "", fmt.Errorf("packed library exports contains URL-normalized whitespace or controls: %w", errs.ErrValidation)
	}

	for segment := range strings.SplitSeq(strings.TrimPrefix(entrypoint, "./"), "/") {
		if segment == "" || segment == "." || segment == ".." || strings.EqualFold(segment, "node_modules") {
			return "", fmt.Errorf("packed library exports contains an invalid path segment: %w", errs.ErrValidation)
		}
	}

	return entrypoint, nil
}
