// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

func TestIsolatedNPMVersionSemanticsPreserveScriptsAndDependencies(t *testing.T) {
	t.Parallel()
	for _, file := range []string{"package.json", "package-lock.json"} {
		body := `{"name":"fixture","version":"1.2.2","scripts":{"release":"echo 1.2.2"},"dependencies":{"other":"1.2.2"}}`
		if file == "package-lock.json" {
			body = `{"name":"fixture","version":"1.2.2","lockfileVersion":3,"packages":{"":{"name":"fixture","version":"1.2.2"},"node_modules/other":{"version":"1.2.2","integrity":"unchanged"}}}`
		}
		before, err := normalizedBumpDocument(projecttype.NPM, file, []byte(body), "1.2.2", nil)
		if err != nil {
			t.Fatal(err)
		}
		updated := strings.Replace(body, `"version":"1.2.2"`, `"version":"1.2.3"`, 1)
		if file == "package-lock.json" {
			updated = strings.Replace(updated, `"version":"1.2.2"`, `"version":"1.2.3"`, 1)
		}
		after, err := normalizedBumpDocument(projecttype.NPM, file, []byte(updated), "1.2.3", nil)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("valid %s bump refused: %v", file, err)
		}
		malicious := strings.ReplaceAll(body, "1.2.2", "1.2.3")
		changed, err := normalizedBumpDocument(projecttype.NPM, file, []byte(malicious), "1.2.3", nil)
		if err != nil {
			t.Fatal(err)
		}
		if reflect.DeepEqual(before, changed) {
			t.Fatalf("%s admitted changes to non-project versions/scripts", file)
		}
	}
}

func TestIsolatedCargoLockOnlyMovesDeclaredWorkspacePackages(t *testing.T) {
	t.Parallel()
	const body = `version = 4
[[package]]
name = "worker"
version = "1.2.2"
dependencies = ["keytool", "external"]
[[package]]
name = "keytool"
version = "1.2.2"
[[package]]
name = "external"
version = "1.2.2"
source = "registry+https://example.invalid/index"
checksum = "unchanged"
`
	names := map[string]bool{"worker": true, "keytool": true}
	before, err := normalizedBumpDocument(projecttype.Cargo, "Cargo.lock", []byte(body), "1.2.2", names)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(body, `version = "1.2.2"`, `version = "1.2.3"`, 2)
	after, err := normalizedBumpDocument(projecttype.Cargo, "Cargo.lock", []byte(updated), "1.2.3", names)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("valid workspace lock update refused: %v", err)
	}
	changed, err := normalizedBumpDocument(projecttype.Cargo, "Cargo.lock", []byte(strings.ReplaceAll(body, "1.2.2", "1.2.3")), "1.2.3", names)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(before, changed) {
		t.Fatal("registry dependency upgrade disguised as a workspace release")
	}
	if _, err := normalizedBumpDocument(projecttype.Cargo, "Cargo.lock", []byte(body), "1.2.3", names); err == nil {
		t.Fatal("stale workspace lock accepted")
	}
}

func TestIsolatedCargoNamesUsesExplicitInheritedWorkspaceMembers(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	for _, name := range []string{"worker", "keytool"} {
		if err := os.MkdirAll(filepath.Join(directory, name), 0o700); err != nil {
			t.Fatal(err)
		}
		body := "[package]\nname = \"" + name + "\"\nversion.workspace = true\n"
		if err := os.WriteFile(filepath.Join(directory, name, "Cargo.toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	names, err := isolatedCargoNames(root, ".", []byte("[workspace]\nmembers = [\"worker\", \"keytool\"]\n[workspace.package]\nversion = \"1.2.2\"\n"))
	if err != nil || !reflect.DeepEqual(names, map[string]bool{"worker": true, "keytool": true}) {
		t.Fatalf("names=%v error=%v", names, err)
	}
}
