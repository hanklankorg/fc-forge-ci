// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package conformance_test

import (
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provenance"
)

// checkCLIIdentity consumes already authenticated downloads. Signature policy is
// owned by VerifyBlob; this fixture only checks the promised payload semantics.
func checkCLIIdentity(kind, version, prepared string, admitted []byte, payloads map[string][]byte) error {
	binary := map[string]string{"go": "forge-ci", "cargo": "nanolinter"}[kind]
	if binary == "" {
		return fmt.Errorf("unsupported CLI kind %q", kind)
	}
	subjects, err := provenance.ParseChecksums(bytes.NewReader(payloads["checksums.txt"]))
	if err != nil {
		return err
	}
	digests := map[string]string{}
	for _, subject := range subjects {
		if _, exists := digests[subject.Name]; exists {
			return fmt.Errorf("duplicate checksum subject %q", subject.Name)
		}
		digests[subject.Name] = subject.SHA256
	}
	for _, name := range []string{binary, "source-identity.json", "build-identity.json"} {
		if len(payloads[name]) == 0 || digests[name] != fmt.Sprintf("%x", sha256.Sum256(payloads[name])) {
			return fmt.Errorf("payload %s not covered by authenticated checksums", name)
		}
	}
	var source struct {
		Commit       string `json:"commit"`
		ObjectFormat string `json:"objectFormat"`
	}
	if err := json.Unmarshal(admitted, &source); err != nil {
		return err
	}
	wantLength := map[string]int{"sha1": 40, "sha256": 64}[source.ObjectFormat]
	if wantLength == 0 || len(source.Commit) != wantLength || strings.Trim(source.Commit, "0123456789abcdef") != "" {
		return fmt.Errorf("invalid admitted source identity")
	}
	var downloadedSource any
	var expectedSource any
	if err := json.Unmarshal(payloads["source-identity.json"], &downloadedSource); err != nil {
		return err
	}
	if err := json.Unmarshal(admitted, &expectedSource); err != nil {
		return err
	}
	want, _ := json.Marshal(expectedSource)
	got, _ := json.Marshal(downloadedSource)
	if !bytes.Equal(want, got) {
		return fmt.Errorf("published source identity differs from admitted request")
	}
	var build struct {
		Version  string `json:"version"`
		Prepared string `json:"preparedRelease"`
		Binary   string `json:"binary"`
		SHA256   string `json:"sha256"`
	}
	if err := json.Unmarshal(payloads["build-identity.json"], &build); err != nil {
		return err
	}
	if build.Version != version || build.Prepared != prepared || build.Binary != binary || build.SHA256 != fmt.Sprintf("%x", sha256.Sum256(payloads[binary])) {
		return fmt.Errorf("published build identity does not match release/payload")
	}
	image, err := elf.NewFile(bytes.NewReader(payloads[binary]))
	if err != nil {
		return fmt.Errorf("CLI is not ELF: %w", err)
	}
	defer image.Close()
	if image.Machine != elf.EM_X86_64 || image.Class != elf.ELFCLASS64 || image.Data != elf.ELFDATA2LSB || (image.Type != elf.ET_EXEC && image.Type != elf.ET_DYN) {
		return fmt.Errorf("CLI is not a Linux amd64 executable")
	}
	return nil
}

func TestCLIIdentityRejectsSubstitutions(t *testing.T) {
	// A tiny ELF64 executable header is sufficient for structural inspection;
	// no compiler or product executable is invoked.
	binary := make([]byte, 64)
	copy(binary, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary[16], binary[18], binary[20], binary[52] = 2, 62, 1, 64
	source := []byte(`{"commit":"` + strings.Repeat("a", 64) + `","objectFormat":"sha256"}`)
	prepared := strings.Repeat("b", 64)
	for _, kind := range []string{"go", "cargo"} {
		name := map[string]string{"go": "forge-ci", "cargo": "nanolinter"}[kind]
		build, _ := json.Marshal(map[string]string{"version": "0.8.6", "preparedRelease": prepared, "binary": name, "sha256": fmt.Sprintf("%x", sha256.Sum256(binary))})
		fresh := func() map[string][]byte {
			p := map[string][]byte{name: bytes.Clone(binary), "source-identity.json": bytes.Clone(source), "build-identity.json": bytes.Clone(build)}
			for _, file := range []string{name, "source-identity.json", "build-identity.json"} {
				p["checksums.txt"] = fmt.Appendf(p["checksums.txt"], "%x  %s\n", sha256.Sum256(p[file]), file)
			}
			return p
		}
		if err := checkCLIIdentity(kind, "0.8.6", prepared, source, fresh()); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{name, "source-identity.json", "build-identity.json", "checksums.txt"} {
			t.Run(kind+"/"+field, func(t *testing.T) {
				p := fresh()
				p[field] = []byte("substituted")
				if checkCLIIdentity(kind, "0.8.6", prepared, source, p) == nil {
					t.Fatal("accepted substitution")
				}
			})
		}
		duplicate := fresh()
		duplicate["checksums.txt"] = append(duplicate["checksums.txt"], duplicate["checksums.txt"]...)
		if checkCLIIdentity(kind, "0.8.6", prepared, source, duplicate) == nil {
			t.Fatal("accepted duplicate checksum subjects")
		}
		if checkCLIIdentity(kind, "0.8.7", prepared, source, fresh()) == nil || checkCLIIdentity(kind, "0.8.6", strings.Repeat("c", 64), source, fresh()) == nil {
			t.Fatal("accepted unrelated release")
		}
		// Internally consistent, authenticated substitutions must also fail
		// semantic checks; digest verification alone is insufficient.
		for _, change := range []string{"source", "architecture", "binary-name"} {
			t.Run(kind+"/authenticated-"+change, func(t *testing.T) {
				p := fresh()
				switch change {
				case "source":
					p["source-identity.json"] = bytes.ReplaceAll(source, []byte("a"), []byte("c"))
				case "architecture":
					p[name][18] = 183 // AArch64, despite the amd64 claim.
					p["build-identity.json"] = bytes.ReplaceAll(build, []byte(fmt.Sprintf("%x", sha256.Sum256(binary))), []byte(fmt.Sprintf("%x", sha256.Sum256(p[name]))))
				case "binary-name":
					p["build-identity.json"] = bytes.ReplaceAll(build, []byte(name), []byte("unrelated-cli"))
				}
				p["checksums.txt"] = nil
				for _, file := range []string{name, "source-identity.json", "build-identity.json"} {
					p["checksums.txt"] = fmt.Appendf(p["checksums.txt"], "%x  %s\n", sha256.Sum256(p[file]), file)
				}
				if checkCLIIdentity(kind, "0.8.6", prepared, source, p) == nil {
					t.Fatal("accepted authenticated identity substitution")
				}
			})
		}
	}
}
