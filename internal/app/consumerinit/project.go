// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package consumerinit

import (
	"encoding/json"
	"encoding/xml"
	"io/fs"
	"path"
	"regexp"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"

	domain "github.com/diggsweden/forge-ci/internal/domain/consumerinit"
)

// DetectProject reads the project's name and, for Go, its main package from
// the build files at the root of fsys. What it cannot read stays empty, and
// the placeholder it would have filled is reported for a person instead. It
// reads only; a malformed build file is the same as no answer.
func DetectProject(fsys fs.FS) domain.Project {
	var project domain.Project

	if data, err := fs.ReadFile(fsys, "go.mod"); err == nil {
		if mod := modfile.ModulePath(data); mod != "" {
			prefix, _, ok := module.SplitPathVersion(mod)
			if !ok {
				prefix = mod
			}

			project.Name = path.Base(prefix)
			project.MainPackage = goMainPackage(fsys, project.Name)
		}

		return project
	}

	switch {
	case exists(fsys, "package.json"):
		project.Name = npmName(fsys)
	case exists(fsys, "Cargo.toml"):
		project.Name = tomlPackageName(fsys, "Cargo.toml")
	case exists(fsys, "pom.xml"):
		project.Name = pomArtifactID(fsys)
	case exists(fsys, "settings.gradle.kts"):
		project.Name = gradleRootName(fsys, "settings.gradle.kts")
	case exists(fsys, "settings.gradle"):
		project.Name = gradleRootName(fsys, "settings.gradle")
	}

	return project
}

func exists(fsys fs.FS, name string) bool {
	_, err := fs.Stat(fsys, name)

	return err == nil
}

// goMainPackage is "." when the module root holds package main, else the
// single ./cmd/<x> that does (or the one named after the module when there
// are several), else unknown.
func goMainPackage(fsys fs.FS, name string) string {
	if isMainPackage(fsys, ".") {
		return "."
	}

	entries, err := fs.ReadDir(fsys, "cmd")
	if err != nil {
		return ""
	}

	var mains []string

	for _, e := range entries {
		if e.IsDir() && isMainPackage(fsys, path.Join("cmd", e.Name())) {
			mains = append(mains, e.Name())
		}
	}

	switch {
	case len(mains) == 1:
		return "./cmd/" + mains[0]
	case len(mains) > 1:
		for _, m := range mains {
			if m == name {
				return "./cmd/" + m
			}
		}
	}

	return ""
}

var packageMain = regexp.MustCompile(`(?m)^package\s+main\s*$`)

func isMainPackage(fsys fs.FS, dir string) bool {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return false
	}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		if data, err := fs.ReadFile(fsys, path.Join(dir, name)); err == nil && packageMain.Match(data) {
			return true
		}
	}

	return false
}

func npmName(fsys fs.FS) string {
	data, err := fs.ReadFile(fsys, "package.json")
	if err != nil {
		return ""
	}

	var pkg struct {
		Name string `json:"name"`
	}

	if json.Unmarshal(data, &pkg) != nil {
		return ""
	}

	// @scope/name publishes as name's tarball; the artifact is the name.
	return path.Base(pkg.Name)
}

var tomlName = regexp.MustCompile(`^\s*name\s*=\s*"([^"]+)"`)

// tomlPackageName reads name from the [package] table. Cargo.toml is TOML;
// one key in one table does not need a TOML parser.
func tomlPackageName(fsys fs.FS, file string) string {
	data, err := fs.ReadFile(fsys, file)
	if err != nil {
		return ""
	}

	inPackage := false

	for line := range strings.SplitSeq(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			inPackage = trimmed == "[package]"

			continue
		}

		if m := tomlName.FindStringSubmatch(line); inPackage && m != nil {
			return m[1]
		}
	}

	return ""
}

func pomArtifactID(fsys fs.FS) string {
	data, err := fs.ReadFile(fsys, "pom.xml")
	if err != nil {
		return ""
	}

	var pom struct {
		ArtifactID string `xml:"artifactId"`
	}

	if xml.Unmarshal(data, &pom) != nil {
		return ""
	}

	return strings.TrimSpace(pom.ArtifactID)
}

var gradleRoot = regexp.MustCompile(`(?m)^\s*rootProject\.name\s*=\s*["']([^"']+)["']`)

func gradleRootName(fsys fs.FS, file string) string {
	data, err := fs.ReadFile(fsys, file)
	if err != nil {
		return ""
	}

	if m := gradleRoot.FindSubmatch(data); m != nil {
		return string(m[1])
	}

	return ""
}
