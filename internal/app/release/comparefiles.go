// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"crypto/sha256"
	"encoding/hex"
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

	left, err := releaseInventory(first, expected, inventory)
	if err != nil {
		return err
	}

	right, err := releaseInventory(second, expected, inventory)
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

// releaseInventory lists the files in directory that an inventory pattern
// selects, and requires a one-to-one match between them and expected.
func releaseInventory(directory string, expected, inventory []string) ([]string, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}

	var files []string

	for _, entry := range entries {
		selected, err := matchesAny(inventory, entry.Name())
		if err != nil {
			return nil, err
		}

		if selected {
			files = append(files, entry.Name())
		}
	}

	if len(files) != len(expected) {
		return nil, fmt.Errorf("expected %d release files in %s; found %d: %w", len(expected), directory, len(files), errs.ErrValidation)
	}

	for _, pattern := range expected {
		count, err := namesMatching(pattern, files)
		if err != nil {
			return nil, err
		}

		if count != 1 {
			return nil, fmt.Errorf("expected exactly one %s in %s; found %d: %w", pattern, directory, count, errs.ErrValidation)
		}
	}

	// Every expected pattern is valid here: the loop above matched each one.
	for _, name := range files {
		count := 0

		for _, pattern := range expected {
			if matched, _ := filepath.Match(pattern, name); matched {
				count++
			}
		}

		if count != 1 {
			return nil, fmt.Errorf("release file %s must match exactly one expected pattern: %w", name, errs.ErrValidation)
		}
	}

	return files, nil
}

// matchesAny reports whether one of patterns matches name, checking the
// patterns in order and stopping at the first match.
func matchesAny(patterns []string, name string) (bool, error) {
	for _, pattern := range patterns {
		matched, err := filepath.Match(pattern, name)
		if err != nil || matched {
			return matched, err
		}
	}

	return false, nil
}

// namesMatching counts the names that pattern matches.
func namesMatching(pattern string, names []string) (int, error) {
	count := 0

	for _, name := range names {
		matched, err := filepath.Match(pattern, name)
		if err != nil {
			return 0, err
		}

		if matched {
			count++
		}
	}

	return count, nil
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

	return hex.EncodeToString(hash.Sum(nil)), nil
}
