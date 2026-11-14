// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestReleaseReadbackAuthenticatesCapturedChecksums(t *testing.T) {
	for _, retain := range []bool{false, true} {
		for _, substitute := range []bool{false, true} {
			t.Run(fmt.Sprintf("retain=%t/substitute=%t", retain, substitute), func(t *testing.T) {
				t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", "")
				t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE", "")

				if retain {
					t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", t.TempDir())
				}

				names := []string{"app.tar.gz", "checksums.txt", "checksums.txt.bundle"}
				if !substitute {
					names = append(names, "app.tar.gz.bundle")
				}

				evidence, err := NewReleaseEvidence(Target{}, "repo", "v1", "request", names)
				if err != nil {
					t.Fatal(err)
				}

				manifestA := fmt.Appendf(nil, "%x  app.tar.gz\n", sha256.Sum256([]byte("original")))
				manifestB := fmt.Appendf(nil, "%x  app.tar.gz\n", sha256.Sum256([]byte("substitute")))
				calls := map[string]int{}
				download := func(name string) ([]byte, error) {
					calls[name]++

					if name == "checksums.txt" {
						if calls[name] > 1 {
							return manifestB, nil
						}

						return manifestA, nil
					}

					if name == "app.tar.gz" && substitute {
						return []byte("substitute"), nil
					}

					if name == "app.tar.gz" {
						return []byte("original"), nil
					}

					return []byte(name), nil
				}

				verified, err := VerifyReleaseReadback(evidence, names, download, func(body, signature []byte) error {
					if !substitute && string(body) == "original" && string(signature) == "app.tar.gz.bundle" {
						return nil
					}

					if !bytes.Equal(body, manifestA) || string(signature) != "checksums.txt.bundle" {
						return errors.New("inert signature mismatch")
					}

					return nil
				}, noOpenPGP)
				if (err != nil) != substitute || !verified["checksums.txt"] {
					t.Errorf("composed readback: verified=%v err=%v", verified, err)
				}

				for _, name := range names {
					if calls[name] != 1 {
						t.Errorf("%s fetched %d times, want 1", name, calls[name])
					}
				}

				if retain {
					if !evidence.receipt.Complete {
						t.Fatal("readback lost complete observation evidence")
					}

					retained, err := evidence.readRetained("checksums.txt")
					if err != nil || !bytes.Equal(retained, manifestA) {
						t.Fatalf("retained authenticated document changed: %q, %v", retained, err)
					}
				}
			})
		}
	}
}

func TestReleaseReadbackRetainsFailedVerification(t *testing.T) {
	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", t.TempDir())

	names := []string{"checksums.txt", "checksums.txt.bundle", "payload.tar.gz"}

	evidence, err := NewReleaseEvidence(Target{}, "repo", "v1", "request", names)
	if err != nil {
		t.Fatal(err)
	}

	wantErr := errors.New("inert signature failure")

	verified, err := VerifyReleaseReadback(evidence, names, func(name string) ([]byte, error) { return []byte(name), nil }, func([]byte, []byte) error { return wantErr }, noOpenPGP)
	if !errors.Is(err, wantErr) || len(verified) != 0 || !evidence.receipt.Complete {
		t.Fatalf("failed verification lost readback or became trusted: %v, %v, complete=%t", verified, err, evidence.receipt.Complete)
	}
}

// Every file a release offers is signed or listed in the signed checksums;
// the signature file of an offered file is neither and is no payload. A file
// the checksums omit is refused, and so is a signature file of nothing.
func TestReleaseReadbackRefusesAnUnboundFile(t *testing.T) {
	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", "")
	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE", "")

	checksums := fmt.Appendf(nil, "%x  app.jar\n%x  app.deb\n", sha256.Sum256([]byte("app.jar")), sha256.Sum256([]byte("app.deb")))
	bound := []string{"app.jar", "app.deb", "app.deb.sig", "checksums.txt", "checksums.txt.bundle", "provenance.json", "provenance.json.bundle"}

	for name, test := range map[string]struct {
		extra []string
		want  string
	}{
		"bound":                 {},
		"omitted payload":       {[]string{"app-sources.jar"}, `["app-sources.jar"]`},
		"signature of no file":  {[]string{"other.deb.sig"}, `["other.deb.sig"]`},
		"OpenPGP signature":     {[]string{"app.jar.asc"}, ""},
		"OpenPGP of no file":    {[]string{"gone.asc"}, `release OpenPGP signature "gone.asc" has no offered subject "gone"`},
		"two, named in order":   {[]string{"b.txt", "a.txt"}, `["a.txt" "b.txt"]`},
		"bundle without a file": {[]string{"gone.bundle"}, `has no offered subject "gone"`},
	} {
		names := append(slices.Clone(bound), test.extra...)

		evidence, err := NewReleaseEvidence(Target{}, "repo", "v1", "request", names)
		if err != nil {
			t.Fatal(err)
		}

		_, err = VerifyReleaseReadback(evidence, names, func(name string) ([]byte, error) {
			if name == "checksums.txt" {
				return checksums, nil
			}

			return []byte(name), nil
		}, func([]byte, []byte) error { return nil }, func([]byte, []byte) error { return nil })
		if (err == nil) != (test.want == "") || (err != nil && !strings.Contains(err.Error(), test.want)) {
			t.Errorf("%s: %v, want an error naming %q", name, err, test.want)
		}
	}
}

// noOpenPGP is the OpenPGP verifier of a readback that offers no .asc.
func noOpenPGP([]byte, []byte) error {
	return errors.New("the release offers no OpenPGP signature here")
}

// A release whose checksums carry an OpenPGP signature in place of a Cosign
// bundle binds its files through them once the signature verifies with the
// OpenPGP verifier, and refuses them when it does not; the Cosign verifier
// never sees an .asc.
func TestReleaseReadbackAuthenticatesOpenPGPChecksums(t *testing.T) {
	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", "")
	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE", "")

	checksums := fmt.Appendf(nil, "%x  app.jar\n%x  app.cdx.json\n", sha256.Sum256([]byte("app.jar")), sha256.Sum256([]byte("app.cdx.json")))
	names := []string{"app.jar", "app.cdx.json", "app.cdx.json.asc", "checksums.txt", "checksums.txt.asc", "provenance.json", "provenance.json.bundle"}

	for _, valid := range []bool{true, false} {
		evidence, err := NewReleaseEvidence(Target{}, "repo", "v1", "request", names)
		if err != nil {
			t.Fatal(err)
		}

		verified, err := VerifyReleaseReadback(evidence, names, func(name string) ([]byte, error) {
			if name == "checksums.txt" {
				return checksums, nil
			}

			return []byte(name), nil
		}, func(_, signature []byte) error {
			if string(signature) != "provenance.json.bundle" {
				return fmt.Errorf("the Cosign verifier got %s", signature)
			}

			return nil
		}, func(_, signature []byte) error {
			if !valid || !strings.HasSuffix(string(signature), ".asc") {
				return errors.New("inert OpenPGP signature failure")
			}

			return nil
		})
		if valid && (err != nil || !verified["checksums.txt"] || !verified["app.cdx.json"] || !verified["provenance.json"]) {
			t.Errorf("valid OpenPGP signatures: verified=%v err=%v", verified, err)
		}

		if !valid && (err == nil || !strings.Contains(err.Error(), `verify release OpenPGP signature "checksums.txt.asc"`) || verified["checksums.txt"]) {
			t.Errorf("failed OpenPGP signatures: verified=%v err=%v", verified, err)
		}
	}
}

// A release whose declaration signs its files with GPG offers its checksums
// with an OpenPGP signature, and no file with one also with a Cosign bundle.
func TestGPGReleaseFileFindings(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		names []string
		want  string
	}{
		"signed with GPG":     {names: []string{"app.jar", "checksums.txt", "checksums.txt.asc", "app.cdx.json", "app.cdx.json.asc", "provenance.json", "provenance.json.bundle"}},
		"checksums of a file": {names: []string{"app_checksums.txt", "app_checksums.txt.asc"}},
		"signed with Cosign":  {names: []string{"checksums.txt", "checksums.txt.bundle"}, want: "no OpenPGP signature of its checksums"},
		"both on one file":    {names: []string{"checksums.txt", "checksums.txt.asc", "app.cdx.json", "app.cdx.json.asc", "app.cdx.json.bundle"}, want: "app.cdx.json carries both"},
	} {
		findings := GPGReleaseFileFindings(test.names)
		if (len(findings) == 0) != (test.want == "") || (test.want != "" && !strings.Contains(strings.Join(findings, "\n"), test.want)) {
			t.Errorf("%s: findings %q, want one naming %q", name, findings, test.want)
		}
	}
}
