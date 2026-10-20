// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"fmt"
	"io/fs"
	"path"
	"strings"
	"unicode"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// ImageFileNames validates explicitly selected regular-file paths inside an
// image and returns their unique output basenames. The same grammar is used by
// config planning and the offline extraction command.
func ImageFileNames(paths []string) ([]string, error) {
	if len(paths) == 0 || len(paths) > 128 {
		return nil, fmt.Errorf("image extraction requires 1 to 128 paths: %w", errs.ErrUsage)
	}
	names := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, source := range paths {
		relative := strings.TrimPrefix(source, "/")
		name := path.Base(relative)
		if !fs.ValidPath(relative) || relative == "." || strings.ContainsAny(relative, "\\,") || strings.ContainsFunc(relative, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) || seen[name] {
			return nil, fmt.Errorf("image file paths must be clean paths with unique basenames and no whitespace/comma/backslash: %w", errs.ErrValidation)
		}
		seen[name] = true
		names = append(names, name)
	}
	return names, nil
}
