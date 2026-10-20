// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"reflect"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

// The existing text/digest/git checks are shared by all isolated bumps. npm and
// Cargo additionally compare decoded documents so a matching version token in
// a script, dependency or member path cannot become a signed source change.
func captureIsolatedBumpFiles(kind projecttype.Type, directory string, paths []string) (map[string][]byte, error) {
	if kind == projecttype.Maven {
		return nil, nil
	}
	root, err := os.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	before := map[string][]byte{}
	for _, file := range paths {
		if path.Dir(file) != directory {
			return nil, fmt.Errorf("isolated %s bump may change only the selected root's manifest and lockfile: %w", kind, errs.ErrValidation)
		}
		body, err := cliio.ReadFileInRoot(root, file)
		if err != nil {
			return nil, err
		}
		before[file] = body
	}
	return before, nil
}

func verifyIsolatedBumpContent(kind projecttype.Type, directory string, before map[string][]byte, oldVersion, newVersion string) error {
	if kind == projecttype.Maven {
		return nil
	}
	root, err := os.OpenRoot(".")
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	var names map[string]bool
	if kind == projecttype.Cargo {
		names, err = isolatedCargoNames(root, directory, before[path.Join(directory, "Cargo.toml")])
		if err != nil {
			return err
		}
	}
	for file, body := range before {
		after, err := cliio.ReadFileInRoot(root, file)
		if err != nil {
			return err
		}
		old, err := normalizedBumpDocument(kind, path.Base(file), body, oldVersion, names)
		if err != nil {
			return err
		}
		updated, err := normalizedBumpDocument(kind, path.Base(file), after, newVersion, names)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(old, updated) {
			return fmt.Errorf("isolated bump changes more than project versions in %s: %w", file, errs.ErrValidation)
		}
	}
	return nil
}

func normalizedBumpDocument(kind projecttype.Type, name string, body []byte, version string, names map[string]bool) (map[string]any, error) {
	var document map[string]any
	if kind == projecttype.NPM {
		if err := json.Unmarshal(body, &document); err != nil {
			return nil, err
		}
		if name == "package.json" || document["version"] != nil {
			if err := removeExpectedVersion(document, version); err != nil {
				return nil, err
			}
		}
		if name != "package.json" {
			if packages, ok := document["packages"].(map[string]any); ok {
				if project, ok := packages[""].(map[string]any); ok {
					if err := removeExpectedVersion(project, version); err != nil {
						return nil, err
					}
				}
			}
		}
		return document, nil
	}
	if err := toml.Unmarshal(body, &document); err != nil {
		return nil, err
	}
	if name == "Cargo.toml" {
		project, _ := document["package"].(map[string]any)
		if workspace, ok := document["workspace"].(map[string]any); ok {
			if shared, ok := workspace["package"].(map[string]any); ok && shared["version"] != nil {
				project = shared
			}
		}
		if err := removeExpectedVersion(project, version); err != nil {
			return nil, err
		}
		return document, nil
	}
	entries, ok := document["package"].([]any)
	if !ok {
		return nil, fmt.Errorf("Cargo.lock has no package inventory: %w", errs.ErrValidation)
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		item, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid Cargo.lock package: %w", errs.ErrValidation)
		}
		packageName, _ := item["name"].(string)
		if names[packageName] && (item["source"] == nil || item["source"] == "") {
			if err := removeExpectedVersion(item, version); err != nil {
				return nil, err
			}
			seen[packageName] = true
		}
		if dependencies, ok := item["dependencies"].([]any); ok {
			for index, dependency := range dependencies {
				text, _ := dependency.(string)
				words := strings.Fields(text)
				if len(words) == 2 && names[words[0]] && words[1] == version {
					dependencies[index] = words[0] + " @release-version"
				}
			}
		}
	}
	for name := range names {
		if !seen[name] {
			return nil, fmt.Errorf("Cargo.lock omits released package %s: %w", name, errs.ErrValidation)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		a, _ := json.Marshal(entries[i])
		b, _ := json.Marshal(entries[j])
		return string(a) < string(b)
	})
	return document, nil
}

func removeExpectedVersion(document map[string]any, expected string) error {
	if version, ok := document["version"].(string); !ok || version != expected {
		return fmt.Errorf("manifest/lockfile version does not match the root project version %s: %w", expected, errs.ErrValidation)
	}
	delete(document, "version")
	return nil
}

// Cargo's isolated scope is a single package or explicit workspace members that
// inherit [workspace.package].version. Source manifests are read as data only.
func isolatedCargoNames(root *os.Root, directory string, body []byte) (map[string]bool, error) {
	var manifest struct {
		Package struct {
			Name    string `toml:"name"`
			Version any    `toml:"version"`
		} `toml:"package"`
		Workspace struct {
			Members []string `toml:"members"`
			Exclude []string `toml:"exclude"`
			Package struct {
				Version string `toml:"version"`
			} `toml:"package"`
		} `toml:"workspace"`
	}
	if err := toml.Unmarshal(body, &manifest); err != nil {
		return nil, err
	}
	names := map[string]bool{}
	inherits := func(version any) bool { table, ok := version.(map[string]any); return ok && table["workspace"] == true }
	if manifest.Package.Name != "" && (manifest.Workspace.Package.Version == "" || inherits(manifest.Package.Version)) {
		names[manifest.Package.Name] = true
	}
	for _, pattern := range manifest.Workspace.Members {
		matches, err := fs.Glob(root.FS(), path.Join(directory, pattern, "Cargo.toml"))
		if err != nil || len(matches) == 0 {
			return nil, fmt.Errorf("workspace member must resolve inside the checkout: %w", errs.ErrValidation)
		}
		for _, file := range matches {
			relative, _ := strings.CutPrefix(path.Dir(file), strings.TrimSuffix(directory, "/")+"/")
			excluded := false
			for _, exclude := range manifest.Workspace.Exclude {
				if match, _ := path.Match(exclude, relative); match {
					excluded = true
				}
			}
			if excluded {
				continue
			}
			data, err := cliio.ReadFileInRoot(root, file)
			if err != nil {
				return nil, err
			}
			var member struct {
				Package struct {
					Name    string `toml:"name"`
					Version any    `toml:"version"`
				} `toml:"package"`
			}
			if err := toml.Unmarshal(data, &member); err != nil {
				return nil, err
			}
			if inherits(member.Package.Version) {
				names[member.Package.Name] = true
			}
		}
	}
	return names, nil
}
