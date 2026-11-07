// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

func TestReleaseEvidencePersistsBeforeCleanup(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", root)
	target := Target{Forge: provider.ForgeForgejo, Host: "forge.example", Owner: "scratch", Token: "private-provider-credential"}
	payloads := map[string][]byte{
		"archive.tar.gz":       {0, 255, 1, 2},
		"release-files.json":   []byte(`{"assets":["archive.tar.gz"]}`),
		"checksums.txt":        []byte("public checksum\n"),
		"checksums.txt.bundle": []byte(`{"signature":"public"}`),
		"sbom.json":            []byte(`{"bomFormat":"CycloneDX"}`),
		"package.deb":          []byte("public package"),
	}
	cleaned := false
	t.Run("fake provider readback", func(t *testing.T) {
		var names []string
		for name := range payloads {
			names = append(names, name)
		}
		e, err := NewReleaseEvidence(target, "consumer", "v1.2.3", "request-commit", names)
		if err != nil {
			t.Fatal(err)
		}
		// Like the live owner, cleanup is registered first and runs only after
		// the bounded reader has persisted every public asset.
		t.Cleanup(func() {
			body, err := os.ReadFile(filepath.Join(root, e.prefix+"inventory.json"))
			if err != nil {
				t.Fatal(err)
			}
			var receipt struct {
				Complete bool
				Assets   []releaseEvidenceAsset
			}
			if err := json.Unmarshal(body, &receipt); err != nil {
				t.Fatal(err)
			}
			if !receipt.Complete || len(receipt.Assets) != len(payloads) {
				t.Fatal("cleanup preceded complete retention")
			}
			for _, asset := range receipt.Assets {
				got, err := os.ReadFile(filepath.Join(root, asset.File))
				if err != nil || !bytes.Equal(got, payloads[asset.Name]) {
					t.Fatalf("missing readback %s: %v", asset.Name, err)
				}
				if asset.Size != len(got) || asset.SHA256 != fmt.Sprintf("%x", sha256.Sum256(got)) {
					t.Fatal("missing observed size/hash")
				}
			}
			clear(payloads) // fake repository deletion
			cleaned = true
		})
		read := func(name string) ([]byte, error) { return bytes.Clone(payloads[name]), nil }
		for _, name := range names {
			body, err := read(name)
			if err != nil {
				t.Fatal(err)
			}
			if err := e.Retain(name, body); err != nil {
				t.Fatal(err)
			}
		}
	})
	if !cleaned {
		t.Fatal("fake repository not cleaned")
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 7 {
		t.Fatalf("evidence did not survive cleanup: %v, %d files", err, len(files))
	}
	for _, file := range files {
		body, err := os.ReadFile(filepath.Join(root, file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(body, []byte(target.Token)) || bytes.Contains(body, []byte("browser_download_url")) {
			t.Fatal("credential or URL retained")
		}
		if strings.HasSuffix(file.Name(), "inventory.json") {
			for _, coordinate := range []string{"forgejo", "forge.example", "scratch/consumer", "v1.2.3", "request-commit"} {
				if !bytes.Contains(body, []byte(coordinate)) {
					t.Fatalf("missing coordinate %s", coordinate)
				}
			}
		}
	}
}

func TestReleaseEvidenceProviderCoordinates(t *testing.T) {
	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", t.TempDir())
	for _, forge := range []provider.ForgeAPI{provider.ForgeForgejo, provider.ForgeGitLab, provider.ForgeGitHub} {
		e, err := NewReleaseEvidence(Target{Forge: forge, Host: "example.test", Owner: "owner"}, "repo", "v1", "request-sha", []string{"public.json"})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.Retain("public.json", []byte("{}")); err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(filepath.Join(e.root, e.prefix+"inventory.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(body, []byte(`"provider": "`+string(forge)+`"`)) {
			t.Fatalf("wrong provider coordinates: %s", body)
		}
	}
}

func TestReleaseEvidenceExistingEnvironment(t *testing.T) {
	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", "")
	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE", "")
	e, err := NewReleaseEvidence(Target{}, "repo", "v1", "", []string{"a"})
	if err != nil || e.Enabled() {
		t.Fatalf("retention enabled without opt-in: %v", err)
	}
	if err := e.Retain("a", []byte("public")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE", t.TempDir())
	e, err = NewReleaseEvidence(Target{}, "repo", "v1", "", []string{"a"})
	if err != nil || !e.Enabled() {
		t.Fatalf("legacy evidence environment not honored: %v", err)
	}
}

func TestReleaseEvidenceRejectsNamesLimitsAndSecrets(t *testing.T) {
	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", t.TempDir())
	for _, names := range [][]string{{"../escape"}, {"a/b"}, {`a\b`}, {"."}, {""}, {"a\x00b"}, {"a\nb"}, {"a", "a"}, make([]string, 4097)} {
		if _, err := NewReleaseEvidence(Target{}, "repo", "v1", "", names); err == nil {
			t.Fatalf("accepted unsafe inventory %q", names[:1])
		}
	}
	e, err := NewReleaseEvidence(Target{Token: "private-target-token"}, "repo", "v1", "", []string{"a", "b"}, map[string]string{"key": "private-signing-key"})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"private-target-token", "private-signing-key"} {
		if err := e.Retain("a", []byte(body)); err == nil {
			t.Fatal("retained credential")
		}
	}
	if err := e.Retain("unlisted", nil); err == nil {
		t.Fatal("accepted unlisted payload")
	}
	e.remaining = 3
	if err := e.Retain("a", []byte("ab")); err != nil {
		t.Fatal(err)
	}
	if err := e.Retain("a", nil); err == nil {
		t.Fatal("accepted duplicate read")
	}
	if err := e.Retain("b", []byte("ab")); err == nil {
		t.Fatal("accepted aggregate overflow")
	}
	if e.receipt.Complete {
		t.Fatal("partial evidence marked complete")
	}
}

func TestReleaseEvidenceConfinedWriterFailure(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", filepath.Join(root, "link"))
	if _, err := NewReleaseEvidence(Target{}, "repo", "v1", "", []string{"a"}); err == nil {
		t.Fatal("accepted symlink evidence root")
	}
	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", root)
	e, err := NewReleaseEvidence(Target{}, "repo", "v1", "", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	// A persistence failure is returned to the acceptance owner, not logged
	// and ignored; inventory must remain incomplete.
	if err := os.Mkdir(filepath.Join(root, e.prefix+"asset-0000"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := e.Retain("a", []byte("public")); err == nil {
		t.Fatal("ignored persistence failure")
	}
	if e.receipt.Complete {
		t.Fatal("failed retention marked complete")
	}
}

func TestReleaseEvidenceRejectsCheckoutDestinations(t *testing.T) {
	engine, consumer := t.TempDir(), t.TempDir()
	cwd := filepath.Join(engine, "internal", "livetest")
	if err := os.MkdirAll(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(engine, ".git"), []byte("worktree marker"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	t.Setenv("FORGE_CI_LIVE_CONSUMER", consumer)
	for _, repository := range []struct{ name, root string }{{"engine", engine}, {"consumer", consumer}} {
		for _, variable := range []string{"FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", "FORGE_CI_LIVE_SNAPSHOT_EVIDENCE"} {
			t.Run(repository.name+"/"+variable, func(t *testing.T) {
				t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", "")
				t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE", "")
				// Neither existing nor not-yet-created directories may receive
				// the initial receipt (or subsequently any public payload).
				existing, err := os.MkdirTemp(repository.root, "existing-")
				if err != nil {
					t.Fatal(err)
				}
				missing := filepath.Join(existing, "not-created", "evidence")
				for _, destination := range []string{existing, missing} {
					t.Setenv(variable, destination)
					e, err := NewReleaseEvidence(Target{}, "repo", "v1", "request-sha", []string{"public.json"})
					if !errors.Is(err, errJobEvidenceInCheckout) || e != nil {
						t.Fatalf("checkout destination accepted: evidence=%v, err=%v", e, err)
					}
					entries, err := os.ReadDir(existing)
					if err != nil || len(entries) != 0 {
						t.Fatalf("rejected destination created receipt/payload/directories: %v, %v", entries, err)
					}
				}
			})
		}
	}
}

func TestReleaseEvidenceExternalDestinationOverrides(t *testing.T) {
	consumer := t.TempDir()
	t.Setenv("FORGE_CI_LIVE_CONSUMER", consumer)
	for _, variable := range []string{"FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", "FORGE_CI_LIVE_SNAPSHOT_EVIDENCE"} {
		t.Run(variable, func(t *testing.T) {
			t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", "")
			t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE", consumer)
			root := filepath.Join(t.TempDir(), "new", "evidence")
			t.Setenv(variable, root)
			e, err := NewReleaseEvidence(Target{}, "repo", "v1", "request-sha", []string{"public.json"})
			if err != nil {
				t.Fatal(err)
			}
			payload := []byte(`{"signed":"bytes unchanged"}`)
			if err := e.Retain("public.json", payload); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(root, e.receipt.Assets[0].File))
			if err != nil || !bytes.Equal(got, payload) || !e.receipt.Complete {
				t.Fatalf("external evidence not retained intact: %q, %v", got, err)
			}
			entries, err := os.ReadDir(consumer)
			if err != nil || len(entries) != 0 {
				t.Fatalf("overridden consumer destination was modified: %v, %v", entries, err)
			}
		})
	}
}
