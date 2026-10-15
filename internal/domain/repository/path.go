// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package repository holds case-preserving forge repository paths, distinct
// from Git URLs, filesystem paths and container/package names.
package repository

import (
	"fmt"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// Path is an unescaped namespace/name coordinate. Its zero value is invalid.
type Path struct{ raw string }

// String returns the original spelling, including case.
func (p Path) String() string { return p.raw }

// Namespace returns every segment before the repository name (GitLab subgroups
// are part of the namespace, not part of the repository name).
func (p Path) Namespace() string {
	if slash := strings.LastIndexByte(p.raw, '/'); slash >= 0 {
		return p.raw[:slash]
	}

	return ""
}

// Name returns the final path segment.
func (p Path) Name() string { return p.raw[strings.LastIndexByte(p.raw, '/')+1:] }

// Parser is the pure forge-specific repository addressing role. Implementations
// must not read credentials, consult the network or normalize source identity.
type Parser interface {
	ParseRepository(raw string) (Path, error)
}

// Parse checks the common path alphabet supported by GitHub, GitLab and
// Forgejo. It rejects URL escapes, traversal and ambiguous separators without
// trimming or case folding. Leading punctuation is allowed: a repository such
// as .github is a source name, not an OCI repository component.
//
// These are addressing rules for existing repositories. Creation-time reserved
// words, length limits and availability remain the server's responsibility.
func Parse(raw string) (Path, error) {
	if raw == "" {
		return Path{}, fmt.Errorf("repository (owner/name) is required: %w", errs.ErrUsage)
	}

	parts := strings.Split(raw, "/")
	if len(parts) < 2 {
		return Path{}, fmt.Errorf("repository must be namespace/name: %w", errs.ErrUsage)
	}

	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsFunc(part, func(char rune) bool {
			return !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.-", char)
		}) {
			return Path{}, fmt.Errorf("repository must contain literal, non-relative identifier segments: %w", errs.ErrUsage)
		}
	}

	return Path{raw: raw}, nil
}

// ParseOwnerName is Parse for forges with exactly one owner segment.
func ParseOwnerName(raw string) (Path, error) {
	path, err := Parse(raw)
	if err != nil {
		return Path{}, err
	}

	if strings.Contains(path.Namespace(), "/") {
		return Path{}, fmt.Errorf("repository must be owner/name without subgroups: %w", errs.ErrUsage)
	}

	return path, nil
}
