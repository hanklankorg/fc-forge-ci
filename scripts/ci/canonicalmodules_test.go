// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package ci

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/sumdb/dirhash"
)

// Known sums authenticate real Go's file-proxy downloads. A rejecting local
// sumdb bounds missing-sum cases. This is not remote TLS or image qualification.
func canonicalModuleProxy(t *testing.T, root string) (string, string) {
	t.Helper()

	directory := filepath.Join(root, "proxy/example.invalid/tiny/@v")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}

	// This graph-only dependency supplies no package imported by the main
	// module. It is deliberately absent from the proxy: preparation must use
	// the native no-argument selection, not broaden it to "download all".
	mod := "module example.invalid/tiny\n\ngo 1.26.4\n\nrequire example.invalid/graph-only v1.0.0\n"
	canonicalWrite(t, filepath.Join(directory, "v1.0.0.mod"), mod, 0o600)
	canonicalWrite(t, filepath.Join(directory, "v1.0.0.info"), `{"Version":"v1.0.0","Time":"2026-01-01T00:00:00Z"}`, 0o600)
	canonicalWrite(t, filepath.Join(directory, "list"), "v1.0.0\n", 0o600)

	zipPath := filepath.Join(directory, "v1.0.0.zip")

	file, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}

	writer := zip.NewWriter(file)
	for _, entry := range []struct{ name, body string }{{"go.mod", mod}, {"tiny.go", "package tiny\nconst Value = 1\n"}} {
		part, createErr := writer.Create("example.invalid/tiny@v1.0.0/" + entry.name)
		if createErr != nil {
			t.Fatal(createErr)
		}

		if _, writeErr := io.WriteString(part, entry.body); writeErr != nil {
			t.Fatal(writeErr)
		}
	}

	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	zipSum, err := dirhash.HashZip(zipPath, dirhash.Hash1)
	if err != nil {
		t.Fatal(err)
	}

	modSum, err := dirhash.Hash1([]string{"go.mod"}, func(string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(mod)), nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return "file://" + filepath.Join(root, "proxy"), fmt.Sprintf("example.invalid/tiny v1.0.0 %s\nexample.invalid/tiny v1.0.0/go.mod %s\n", zipSum, modSum)
}

func canonicalModuleCompiler(t *testing.T, fixture *canonicalFixture, proxy, sumdb string) {
	t.Helper()

	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}

	cli := filepath.Join(fixture.root, "inert-cli")
	canonicalWrite(t, cli, fixture.cli, 0o600)
	compiler := filepath.Join(fixture.root, "module-test-go")
	// Compiler output is inert. Actual Go downloads in BOTH phases: during the
	// recipe the file proxy is gone, so only the prepared cache can work.
	body := fmt.Sprintf(`#!/bin/bash
set -euo pipefail
if [[ $1 == env ]]; then printf 'go1.26.4\n'; exit; fi
[[ $GOENV == off && $GOWORK == off && $GOTOOLCHAIN == local ]]
[[ -z ${GH_TOKEN-}${GITHUB_TOKEN-}${GOAUTH-}${GOPRIVATE-}${GOINSECURE-} ]]
[[ $GOMODCACHE != %q && $GOCACHE != "$GOMODCACHE" ]]
if [[ $# == 4 && $3 == mod && $4 == download ]]; then
  [[ $GOPROXY == https://proxy.golang.org && $GOSUMDB == sum.golang.org ]]
  # No home Go's telemetry child could write to after the preparation ends.
  [[ ! -e $HOME ]]
  [[ -z $(ls -A "$GOMODCACHE") ]]
  printf download >>%q
  GOPROXY=%q GOSUMDB=%q %q "$@"
  rm -rf -- %q
  exit
fi
printf build >>%q
[[ $GOPROXY == off && $GOSUMDB == off ]]
%q -C "$2" mod download
while [[ $# -gt 0 ]]; do
  if [[ $1 == -o ]]; then cp %q "$2"; exit; fi
  shift
done
exit 99
`, fixture.env["GOMODCACHE"], filepath.Join(fixture.root, "downloads"), proxy, sumdb, goBin,
		filepath.Join(fixture.root, "proxy"), filepath.Join(fixture.root, "builds"), goBin, cli)
	canonicalWrite(t, compiler, body, 0o700)
	fixture.env["GO"] = compiler
}

func TestCanonicalModulePreparation(t *testing.T) {
	for _, scenario := range []string{"cold-offline", "tampered-sum", "missing-mod", "missing-sum", "modified-mod", "modified-sum", "modified-checksum", "incomplete-sum", "fetch-failed", "wrong-identity"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newCanonicalFixture(t)
			proxy, sums := canonicalModuleProxy(t, fixture.root)

			sumdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "inert checksum service", http.StatusServiceUnavailable)
			}))
			defer sumdb.Close()

			canonicalWrite(t, filepath.Join(fixture.engine, "go.mod"), "module example.invalid/inert\n\ngo 1.26.4\n\nrequire example.invalid/tiny v1.0.0\n", 0o600)

			if scenario == "tampered-sum" {
				sums = strings.ReplaceAll(sums, "h1:", "h1:wrong")
			}

			if scenario == "incomplete-sum" {
				sums = ""
			}

			canonicalWrite(t, filepath.Join(fixture.engine, "go.sum"), sums, 0o600)
			canonicalGit(t, fixture.engine, "add", "go.mod", "go.sum")
			canonicalGit(t, fixture.engine, "-c", "user.name=Offline", "-c", "user.email=offline@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "module fixture")
			fixture.env["ENGINE_COMMIT"] = canonicalGit(t, fixture.engine, "rev-parse", "HEAD")
			// Fixture construction uses porcelain; only the delivery invocation
			// below must avoid this deliberately installed Git machinery.
			if err := os.Remove(fixture.gitMarker); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}

			fixture.env["GH_TOKEN"] = "inert-not-a-secret"
			fixture.env["GOMODCACHE"] = "/unapproved/cache"
			fixture.env["GOSUMDB"] = "off"
			fixture.env["GOINSECURE"] = "*"
			canonicalModuleCompiler(t, fixture, proxy, "sum.golang.org "+sumdb.URL)
			canonicalModuleFailure(t, fixture, scenario)

			out, err := fixture.run(t, "build")
			if scenario == "cold-offline" {
				if err != nil {
					t.Fatalf("cold download then offline recipe: %v\n%s", err, out)
				}

				for _, stage := range []string{"downloads", "builds"} {
					if _, err := os.Stat(filepath.Join(fixture.root, stage)); err != nil {
						t.Fatalf("successful cold fixture omitted %s: %v", stage, err)
					}
				}
			} else {
				if err == nil {
					t.Fatalf("accepted %s: %s", scenario, out)
				}

				for _, path := range []string{fixture.binary, fixture.env["GITHUB_OUTPUT"], filepath.Join(fixture.root, "builds")} {
					if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
						t.Fatalf("build/upload effect before successful preparation: %s (%v)\n%s", path, statErr, out)
					}
				}
			}

			for _, path := range []string{fixture.marker, fixture.helperMarker} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("preparation executed CLI/helper: %s (%v)", path, err)
				}
			}

			matches, globErr := filepath.Glob(filepath.Join(fixture.root, "forge-ci-canonical-modules.*"))
			if globErr != nil || len(matches) != 0 {
				t.Fatalf("private preparation material persisted: %v (%v)", matches, globErr)
			}
		})
	}
}

func canonicalModuleFailure(t *testing.T, fixture *canonicalFixture, scenario string) {
	t.Helper()

	switch scenario {
	case "missing-mod", "missing-sum":
		name := "go.mod"
		if scenario == "missing-sum" {
			name = "go.sum"
		}

		if err := os.Remove(filepath.Join(fixture.engine, name)); err != nil {
			t.Fatal(err)
		}
	case "fetch-failed":
		if err := os.Remove(filepath.Join(fixture.root, "proxy/example.invalid/tiny/@v/v1.0.0.zip")); err != nil {
			t.Fatal(err)
		}
	case "modified-mod":
		canonicalWrite(t, filepath.Join(fixture.engine, "go.mod"), "module attacker.invalid/modified\n", 0o600)
	case "modified-sum":
		canonicalWrite(t, filepath.Join(fixture.engine, "go.sum"), "tampered\n", 0o600)
	case "modified-checksum":
		canonicalWrite(t, filepath.Join(fixture.engine, ".engine/bin/forge-ci-linux-amd64.sha256"), "tampered\n", 0o600)
	case "wrong-identity":
		fixture.env["ENGINE_COMMIT"] = strings.Repeat("e", 40)
	}
}

func TestCanonicalModuleDownloadCannotRepairLock(t *testing.T) {
	fixture := newCanonicalFixture(t)
	compiler := filepath.Join(fixture.root, "rewriting-go")
	canonicalWrite(t, compiler, "#!/bin/bash\nset -eu\nif [[ $1 == env ]]; then printf 'go1.26.4\\n'; exit; fi\n[[ $3 == mod && $4 == download ]]\nprintf repaired >>\"$2/go.sum\"\n", 0o700)
	fixture.env["GO"] = compiler

	out, err := fixture.run(t, "build")
	if err == nil || !strings.Contains(string(out), "module preparation changed go.sum") {
		t.Fatalf("module lock repair accepted: %v\n%s", err, out)
	}

	for _, path := range []string{fixture.binary, fixture.env["GITHUB_OUTPUT"]} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("build/upload effect after lock repair: %s (%v)", path, err)
		}
	}
}

func TestCanonicalModuleWorkflowOrdering(t *testing.T) {
	body, err := os.ReadFile("../../.github/workflows/canonical-cli-bootstrap.yml")
	if err != nil {
		t.Fatal(err)
	}

	last := -1

	for _, boundary := range []string{"Checkout approved engine only", "ref: ${{ inputs.engine-commit }}", "persist-credentials: false", "GO: /usr/local/go/bin/go", "canonical-cli-delivery.sh\" build", "Upload exactly the canonical Linux files", "steps.build.outputs.artifact-name"} {
		index := strings.Index(string(body)[last+1:], boundary)
		if index < 0 {
			t.Fatalf("missing/out-of-order workflow boundary: %s", boundary)
		}

		last += index + 1
	}

	body, err = os.ReadFile("canonical-cli-delivery.sh")
	if err != nil {
		t.Fatal(err)
	}

	last = -1

	for _, boundary := range []string{"wrong engine checkout", "wrong engine origin", "files+=(go.mod go.sum)", "modified engine file:", "mktemp -d", "mod download\n", "module preparation changed", "engine-binary.sh\" build \"$arch\"", "engine-binary.sh\" verify \"$arch\"", "GITHUB_OUTPUT:?"} {
		index := strings.Index(string(body), boundary)
		if index <= last {
			t.Fatalf("missing/out-of-order preparation boundary: %s", boundary)
		}

		last = index
	}

	for _, forbidden := range []string{"GITHUB_ENV", "go env -w", "GOINSECURE=", "GONOSUMDB=", "mod download all"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("persistent or unauthenticated module configuration: %s", forbidden)
		}
	}
}
