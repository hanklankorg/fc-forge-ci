// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"maps"
	"reflect"
	"strings"
	"testing"
)

func TestRustFixtureArtifactsAndImageBytes(t *testing.T) {
	t.Parallel()

	roles := rustFixtureRoles()

	const commit = "1234567890abcdef1234567890abcdef12345678"

	files := rustArtifactFixture(t, roles, commit)

	for _, prefix := range []string{"", "development-artifacts/"} {
		decoded, err := roles.archive(rustFixtureZIP(t, files, prefix))
		if err != nil {
			t.Fatal(err)
		}

		if err := roles.validateArtifacts(decoded, commit); err != nil {
			t.Fatal(err)
		}
	}

	receipt := rustFixtureHashes(files)

	want := map[string][]string{
		"worker":         {"worker", "usr/local/bin/key-tool"},
		"api-service":    {"api-service"},
		"load-generator": {"usr/local/bin/load-generator"},
	}
	if !reflect.DeepEqual(roles.imagePaths(), want) {
		t.Fatalf("fixture image paths = %v, want %v", roles.imagePaths(), want)
	}

	for _, paths := range roles.imagePaths() {
		hashes := map[string]string{}

		for _, path := range paths {
			parts := strings.Split(path, "/")
			hashes[path] = receipt["linux-amd64/"+parts[len(parts)-1]+"-linux-amd64"]
		}

		if err := verifyRustImageHashes(receipt, paths, hashes); err != nil {
			t.Fatal(err)
		}

		for _, mutation := range []string{"changed", "missing", "unexpected"} {
			tampered := maps.Clone(hashes)

			switch mutation {
			case "changed":
				tampered[paths[0]] = strings.Repeat("f", 64)
			case "missing":
				delete(tampered, paths[0])
			case "unexpected":
				tampered["other"] = strings.Repeat("f", 64)
			}

			if err := verifyRustImageHashes(receipt, paths, tampered); err == nil {
				t.Fatalf("%s image bytes accepted", mutation)
			}
		}
	}

	if err := roles.validateArtifacts(files, strings.Repeat("a", 40)); err == nil {
		t.Fatal("receipt for a different commit accepted")
	}

	binaryName := "linux-amd64/" + roles.Tool + "-linux-amd64"
	files[binaryName] = []byte("tampered")

	if err := roles.validateArtifacts(files, commit); err == nil {
		t.Fatal("binary differing from its receipt accepted")
	}

	// Updating the receipt's hashes cannot turn non-ELF bytes into
	// executable evidence or admit another architecture.
	sealRustFixture(t, files, commit)

	if err := roles.validateArtifacts(files, commit); err == nil {
		t.Fatal("non-ELF binary accepted after updating its receipt")
	}

	files[binaryName] = minimalRustELF()
	binary.LittleEndian.PutUint16(files[binaryName][18:20], 183) // EM_AARCH64
	sealRustFixture(t, files, commit)

	if err := roles.validateArtifacts(files, commit); err == nil {
		t.Fatal("wrong executable architecture accepted")
	}

	delete(files, binaryName)
	files["linux-amd64/unexpected-linux-amd64"] = minimalRustELF()

	if _, err := roles.archive(rustFixtureZIP(t, files, "")); err == nil {
		t.Fatal("artifact outside the fixed fixture layout accepted")
	}
}

func rustArtifactFixture(t *testing.T, roles rustRoles, commit string) map[string][]byte {
	t.Helper()

	files := map[string][]byte{}
	for role := range roles.imagePaths() {
		files[role+"-image.json"] = []byte(`{}`)
	}

	for _, name := range []string{roles.Worker, roles.Tool, roles.Service, roles.LoadGenerator} {
		files["linux-amd64/"+name+"-linux-amd64"] = minimalRustELF()
	}

	sealRustFixture(t, files, commit)

	return files
}

func minimalRustELF() []byte {
	body := make([]byte, 64)
	copy(body, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(body[16:18], 2)  // ET_EXEC
	binary.LittleEndian.PutUint16(body[18:20], 62) // EM_X86_64
	binary.LittleEndian.PutUint32(body[20:24], 1)
	binary.LittleEndian.PutUint16(body[52:54], 64)

	return body
}

func rustFixtureHashes(files map[string][]byte) map[string]string {
	hashes := map[string]string{}

	for name, body := range files {
		if name != "receipt.json" {
			sum := sha256.Sum256(body)
			hashes[name] = hex.EncodeToString(sum[:])
		}
	}

	return hashes
}

func sealRustFixture(t *testing.T, files map[string][]byte, commit string) {
	t.Helper()

	body, err := json.Marshal(map[string]any{
		"sourceSHA": commit, "version": "0.1.0-dev." + commit[:12], "files": rustFixtureHashes(files),
	})
	if err != nil {
		t.Fatal(err)
	}

	files["receipt.json"] = body
}

func rustFixtureZIP(t *testing.T, files map[string][]byte, prefix string) []byte {
	t.Helper()

	var buffer bytes.Buffer

	writer := zip.NewWriter(&buffer)
	for name, body := range files {
		entry, err := writer.Create(prefix + name)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := entry.Write(body); err != nil {
			t.Fatal(err)
		}
	}

	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	return buffer.Bytes()
}
