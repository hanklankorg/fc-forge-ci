// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
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
		names := make([]string, 0, len(payloads))
		for name := range payloads {
			names = append(names, name)
		}

		evidence, err := NewReleaseEvidence(target, "consumer", "v1.2.3", "request-commit", names)
		if err != nil {
			t.Fatal(err)
		}
		// Like the live owner, cleanup is registered first and runs only after
		// the bounded reader has persisted every public asset.
		t.Cleanup(func() {
			body, err := os.ReadFile(filepath.Join(root, evidence.prefix+"inventory.json"))
			if err != nil {
				t.Fatal(err)
			}

			var receipt struct {
				Complete bool                   `json:"complete"`
				Assets   []releaseEvidenceAsset `json:"assets"`
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

		for _, name := range names {
			if err := evidence.Retain(name, bytes.Clone(payloads[name])); err != nil {
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

func TestReleaseEvidenceCaptureReadback(t *testing.T) {
	for _, defect := range []string{"", "download", "budget", "secret", "disabled"} {
		t.Run(defect, func(t *testing.T) {
			t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", t.TempDir())

			if defect == "disabled" {
				t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", "")
				t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE", "")
			}

			evidence, err := NewReleaseEvidence(Target{Token: "inert-private-token"}, "repo", "v1", "request", []string{"a", "b"})
			if err != nil {
				t.Fatal(err)
			}

			if defect == "budget" {
				evidence.remaining = 2
			}

			calls := 0

			readback, err := evidence.CaptureReadback(func(name string) ([]byte, error) {
				calls++

				if name == "b" {
					switch defect {
					case "download":
						return nil, os.ErrNotExist
					case "budget":
						return []byte("too large"), nil
					case "secret":
						return []byte("inert-private-token"), nil
					}
				}

				return []byte(name), nil
			})
			if defect == "download" || defect == "budget" || defect == "secret" {
				if err == nil || evidence.receipt.Complete || !evidence.retained("a") || evidence.retained("b") {
					t.Fatalf("failed capture lost partial inventory or claimed completion: %v", err)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if defect == "disabled" && calls != 0 {
				t.Fatal("disabled capture fetched assets eagerly")
			}

			for _, name := range []string{"a", "b"} {
				body, err := readback(name)
				if err != nil || string(body) != name {
					t.Fatalf("readback %s: %q, %v", name, body, err)
				}
			}

			if calls != 2 {
				t.Fatalf("expected one provider fetch per asset, got %d", calls)
			}

			if defect != "disabled" {
				if _, err := readback("unlisted"); err == nil {
					t.Fatal("read retained unlisted asset")
				}
			}
		})
	}
}

func TestReleaseEvidenceCaptureOnceWithoutRetention(t *testing.T) {
	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", "")
	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE", "")

	evidence, err := NewReleaseEvidence(Target{}, "repo", "v1", "request", []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}

	evidence.remaining = 2
	shared := []byte("a")
	calls := 0

	read, err := evidence.CaptureReadback(func(name string) ([]byte, error) {
		calls++

		if name == "b" {
			return []byte("bb"), nil
		}

		return shared, nil
	})
	if err != nil || calls != 0 {
		t.Fatalf("disabled readback fetched eagerly: %v, calls=%d", err, calls)
	}

	first, err := read("a")
	if err != nil || string(first) != "a" {
		t.Fatalf("first captured read: %q, %v", first, err)
	}

	shared[0], first[0] = 'x', 'y'

	second, err := read("a")
	if err != nil || string(second) != "a" || calls != 1 {
		t.Fatalf("captured bytes changed or were fetched again: %q, %v, calls=%d", second, err, calls)
	}

	if _, err := read("b"); !errors.Is(err, errReleaseEvidence) {
		t.Fatalf("disabled capture ignored aggregate byte budget: %v", err)
	}
}

func TestReleaseEvidenceRejectsNamesLimitsAndSecrets(t *testing.T) {
	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", t.TempDir())

	for _, names := range [][]string{{"../escape"}, {"a/b"}, {`a\b`}, {"."}, {""}, {"a\x00b"}, {"a\nb"}, {"a", "a"}, make([]string, 4097)} {
		if _, err := NewReleaseEvidence(Target{}, "repo", "v1", "", names); err == nil {
			t.Fatalf("accepted unsafe inventory %q", names[:1])
		}
	}

	evidence, err := NewReleaseEvidence(Target{Token: "private-target-token"}, "repo", "v1", "", []string{"a", "b"}, map[string]string{"key": "private-signing-key"})
	if err != nil {
		t.Fatal(err)
	}

	for _, body := range []string{"private-target-token", "private-signing-key"} {
		if err := evidence.Retain("a", []byte(body)); err == nil {
			t.Fatal("retained credential")
		}
	}

	if err := evidence.Retain("unlisted", nil); err == nil {
		t.Fatal("accepted unlisted payload")
	}

	evidence.remaining = 3
	if err := evidence.Retain("a", []byte("ab")); err != nil {
		t.Fatal(err)
	}

	if err := evidence.Retain("a", nil); err == nil {
		t.Fatal("accepted duplicate read")
	}

	if err := evidence.Retain("b", []byte("ab")); err == nil {
		t.Fatal("accepted aggregate overflow")
	}

	if evidence.receipt.Complete {
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
				existing, err := os.MkdirTemp(repository.root, "existing-") //nolint:usetesting // the directory must sit inside the checkout under test.
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
