// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	domainartifact "github.com/diggsweden/forge-ci/internal/domain/artifact"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// CompareReleaseFiles compares the caller's reproducible subject inventory.
// SBOMs/signatures are not implicitly included: callers choose their patterns.
func CompareReleaseFiles(first, second string, expected, inventory []string) error {
	if len(expected) == 0 || len(inventory) == 0 {
		return fmt.Errorf("comparison requires expected and inventory patterns: %w", errs.ErrUsage)
	}

	collect := func(directory string) ([]string, error) {
		entries, err := os.ReadDir(directory)
		if err != nil {
			return nil, err
		}

		var files []string

		for _, entry := range entries {
			for _, pattern := range inventory {
				matched, err := filepath.Match(pattern, entry.Name())
				if err != nil {
					return nil, err
				}

				if matched {
					files = append(files, entry.Name())
					break
				}
			}
		}

		if len(files) != len(expected) {
			return nil, fmt.Errorf("expected %d release files in %s; found %d: %w", len(expected), directory, len(files), errs.ErrValidation)
		}

		for _, pattern := range expected {
			count := 0

			for _, name := range files {
				matched, err := filepath.Match(pattern, name)
				if err != nil {
					return nil, err
				}

				if matched {
					count++
				}
			}

			if count != 1 {
				return nil, fmt.Errorf("expected exactly one %s in %s; found %d: %w", pattern, directory, count, errs.ErrValidation)
			}
		}

		for _, name := range files {
			count := 0

			for _, pattern := range expected {
				matched, _ := filepath.Match(pattern, name)
				if matched {
					count++
				}
			}

			if count != 1 {
				return nil, fmt.Errorf("release file %s must match exactly one expected pattern: %w", name, errs.ErrValidation)
			}
		}

		return files, nil
	}

	left, err := collect(first)
	if err != nil {
		return err
	}

	right, err := collect(second)
	if err != nil {
		return err
	}

	if !slices.Equal(left, right) {
		return fmt.Errorf("different release file inventories: %w", errs.ErrValidation)
	}

	for _, name := range left {
		a, err := reproducibleFileDigest(filepath.Join(first, name))
		if err != nil {
			return err
		}

		b, err := reproducibleFileDigest(filepath.Join(second, name))
		if err != nil {
			return err
		}

		if a != b {
			return fmt.Errorf("different release file %s: %w", name, errs.ErrValidation)
		}
	}

	return nil
}

func reproducibleFileDigest(path string) (string, error) {
	file, err := openReleaseFile(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()

	hash := sha256.New()

	n, err := domainartifact.CopyAtMost(hash, file, domainartifact.MaxFileBytes)
	if err != nil {
		return "", err
	}

	if n == 0 {
		return "", fmt.Errorf("empty release file %s: %w", path, errs.ErrValidation)
	}

	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}
