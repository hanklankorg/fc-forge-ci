// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage

import (
	"fmt"
	"io/fs"
)

const DefaultDependenciesFile = "snapshot-dependencies.json"

// DependencyInventory is a run-local integrity handoff recorded by the trusted
// artifact/context producer. It binds the exact input bytes, not a signature or
// an independent assertion about which CI run downloaded them.
type DependencyInventory struct {
	Version      int                `json:"version"`
	SourceSHA    string             `json:"source_sha"`
	SourceBranch string             `json:"source_branch"`
	Dependencies []DependencyRecord `json:"dependencies"`
}

type DependencyRecord struct {
	ImageName string `json:"image_name"`
	Platform  string `json:"platform"`
	Dependency
	Files []DependencyFile `json:"files"`
}

type DependencyFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   string `json:"mode"`
}

// Permissions accepts only the artifact digest's canonical file modes. In
// particular, missing modes and setuid/setgid/sticky/type bits are never trusted.
func (f DependencyFile) Permissions() (fs.FileMode, error) {
	switch f.Mode {
	case "0644":
		return 0o644, nil
	case "0755":
		return 0o755, nil
	default:
		return 0, fmt.Errorf("snapshot file %s has invalid mode %q (expected 0644 or 0755)", f.Path, f.Mode)
	}
}
