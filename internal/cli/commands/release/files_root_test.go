// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	rootcli "github.com/diggsweden/forge-ci/internal/cli"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

func runReleaseFilesRoot(t *testing.T, args ...string) error {
	t.Helper()

	command := rootcli.New(rootcli.BuildInfo{Version: "dev"})
	command.Writer, command.ErrWriter = os.Stdout, os.Stderr

	return command.Run(t.Context(), append([]string{"forge-ci", "--provider", "forgejo", "--format", "text", "release", "files"}, args...))
}

// producerDist is a producer's dist directory under construction: the files
// it holds, how the manifest classifies them, and which of them the checksums
// file lists.
type producerDist struct {
	env                          *testenv.Env
	assets, evidence, provenance []string
	checksummed                  []string
	checksumsName                string
}

func newProducerDist(t *testing.T) *producerDist {
	t.Helper()

	env := provenanceRootEnv(t, "forgejo")
	env.MkdirAll("dist")

	dist := &producerDist{env: env, checksumsName: "SHA256SUMS"}
	dist.file(t, "app.tgz", true)

	return dist
}

// file writes dist/<name> and declares it as an asset; checksummed also lists
// it in the checksums file.
func (d *producerDist) file(t *testing.T, name string, checksummed bool) {
	t.Helper()

	d.plant(t, name)
	d.assets = append(d.assets, name)

	if checksummed {
		d.checksummed = append(d.checksummed, name)
	}
}

// plant writes dist/<name> without declaring it.
func (d *producerDist) plant(t *testing.T, name string) {
	t.Helper()
	require.NoError(t, os.WriteFile(d.env.Path(filepath.Join("dist", name)), []byte("content of "+name+"\n"), 0o600))
}

// write writes the checksums file and the manifest.
func (d *producerDist) write(t *testing.T) {
	t.Helper()

	var checksums strings.Builder

	for _, name := range d.checksummed {
		body, err := os.ReadFile(d.env.Path(filepath.Join("dist", name)))
		require.NoError(t, err)
		fmt.Fprintf(&checksums, "%x  %s\n", sha256.Sum256(body), name)
	}

	require.NoError(t, os.WriteFile(d.env.Path(filepath.Join("dist", d.checksumsName)), []byte(checksums.String()), 0o600))

	type entry struct {
		Path   string `json:"path"`
		Name   string `json:"name"`
		Source string `json:"source"`
	}

	entries := func(names []string) []entry {
		out := make([]entry, 0, len(names))
		for _, name := range names {
			out = append(out, entry{Path: "dist/" + name, Name: filepath.Base(name), Source: "producer"})
		}

		return out
	}

	body, err := json.Marshal(map[string]any{
		"version":    1,
		"assets":     entries(append([]string{d.checksumsName}, d.assets...)),
		"checksums":  entries([]string{d.checksumsName}),
		"sboms":      []entry{},
		"evidence":   entries(d.evidence),
		"provenance": entries(d.provenance),
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(d.env.Path("dist/release-files.json"), body, 0o600))
}

// Every declared asset is a checksum subject unless the engine defines it as
// release metadata. A classification or a file name a producer chooses does
// not take a file out of the checksums file's coverage.
func TestReleaseFilesRoot_ValidateChecksumsCoversEveryProducerAsset(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arrange func(t *testing.T, dist *producerDist)
		want    error
		message string
	}{
		{
			name:    "complete",
			arrange: func(*testing.T, *producerDist) {},
		},
		{
			name: "asset classified as provenance",
			arrange: func(t *testing.T, dist *producerDist) {
				t.Helper()
				dist.file(t, "installer.tar.gz", false)
				dist.provenance = []string{"installer.tar.gz"}
			},
			want: errs.ErrValidation, message: "provenance must be dist/slsa-provenance.intoto.json, got dist/installer.tar.gz",
		},
		{
			name: "asset named as a signature of nothing declared",
			arrange: func(t *testing.T, dist *producerDist) {
				t.Helper()
				dist.file(t, "installer.sig", false)
				dist.file(t, "payload.bundle", false)
			},
			want: errs.ErrValidation, message: "checksum file is missing public release assets: [installer.sig payload.bundle]",
		},
		{
			name: "second file named like a checksums file",
			arrange: func(t *testing.T, dist *producerDist) {
				t.Helper()
				dist.file(t, "extra_checksums.txt", false)
			},
			want: errs.ErrValidation, message: "checksum file is missing public release assets: [extra_checksums.txt]",
		},
		{
			// The signature of a declared asset is the one name-based
			// exemption, and it is the file finalization adds.
			name: "signature of a declared asset",
			arrange: func(t *testing.T, dist *producerDist) {
				t.Helper()
				dist.file(t, "app.tgz.sig", false)
				dist.file(t, "SHA256SUMS.bundle", false)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dist := newProducerDist(t)
			tc.arrange(t, dist)
			dist.write(t)

			err := runReleaseFilesRoot(t, "validate-checksums")
			if tc.want == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.want)
			require.ErrorContains(t, err, tc.message)
		})
	}
}

// An evidence file is not in the checksums file, which exists before it does.
// Finalization publishes it only with its own signature bundle, and leaves the
// manifest as it was when it refuses.
func TestReleaseFilesRoot_ManifestRefusesUnsignedEvidence(t *testing.T) {
	dist := newProducerDist(t)
	dist.file(t, "installer.tar.gz", false)
	dist.evidence = []string{"installer.tar.gz"}
	dist.write(t)
	dist.plant(t, "slsa-provenance.intoto.json")
	dist.plant(t, "slsa-provenance.intoto.json.bundle")

	require.NoError(t, runReleaseFilesRoot(t, "validate-checksums"))

	before, err := os.ReadFile(dist.env.Path("dist/release-files.json"))
	require.NoError(t, err)

	err = runReleaseFilesRoot(t, "manifest")
	require.ErrorIs(t, err, errs.ErrValidation)
	require.ErrorContains(t, err, "evidence file dist/installer.tar.gz has no signature bundle")

	after, err := os.ReadFile(dist.env.Path("dist/release-files.json"))
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))

	dist.plant(t, "installer.tar.gz.bundle")
	require.NoError(t, runReleaseFilesRoot(t, "manifest"))
	require.NoError(t, runReleaseFilesRoot(t, "validate-checksums"))
	require.NoError(t, runReleaseFilesRoot(t, "validate"))

	// The finalized manifest declares the signatures, so it is no longer the
	// unsigned hand-off.
	require.ErrorIs(t, runReleaseFilesRoot(t, "validate", "--unsigned"), errs.ErrValidation)
}

// Publish uploads the assets of a finalized manifest only. A producer's
// manifest is refused before anything is uploaded, whether or not it names
// evidence: the statement, its bundle and the bundle of each evidence file are
// what finalization adds. The dry run reaches no forge and still refuses.
func TestReleaseRoot_PublishRequiresAFinalizedManifest(t *testing.T) {
	publish := func(t *testing.T) error {
		t.Helper()

		command := rootcli.New(rootcli.BuildInfo{Version: "dev"})
		command.Writer, command.ErrWriter = os.Stdout, os.Stderr

		return command.Run(t.Context(), []string{
			"forge-ci", "--provider", "forgejo", "--format", "text", "release", "publish", "--dry-run",
			"--tag", "v1.2.3", "--repository", "owner/app", "--release-notes-file", "notes.md",
		})
	}

	dist := newProducerDist(t)
	dist.file(t, "installer.tar.gz", false)
	dist.evidence = []string{"installer.tar.gz"}
	dist.write(t)
	require.NoError(t, os.WriteFile(dist.env.Path("notes.md"), []byte("notes\n"), 0o600))

	err := publish(t)
	require.ErrorIs(t, err, errs.ErrValidation)
	require.ErrorContains(t, err, "is not finalized: it declares no provenance statement")

	// Signing has run, finalization has not: the files exist and the manifest
	// does not declare them.
	for _, name := range []string{"slsa-provenance.intoto.json", "slsa-provenance.intoto.json.bundle", "installer.tar.gz.bundle"} {
		dist.plant(t, name)
	}

	require.ErrorIs(t, publish(t), errs.ErrValidation)

	// A manifest that declares the statement and its bundle still has to
	// declare the bundle of its evidence.
	dist.assets = append(dist.assets, "slsa-provenance.intoto.json", "slsa-provenance.intoto.json.bundle")
	dist.provenance = []string{"slsa-provenance.intoto.json"}
	dist.write(t)

	err = publish(t)
	require.ErrorIs(t, err, errs.ErrValidation)
	require.ErrorContains(t, err, "declares dist/installer.tar.gz without its signature bundle dist/installer.tar.gz.bundle")

	dist.assets = dist.assets[:len(dist.assets)-2]
	dist.provenance = nil
	dist.write(t)

	require.NoError(t, runReleaseFilesRoot(t, "manifest"))
	require.NoError(t, publish(t))
}

// The hand-off check refuses everything finalization would otherwise publish
// as the signer's: a declared provenance statement or signature, and a
// statement or signature already in the dist.
func TestReleaseFilesRoot_ValidateUnsignedRefusesProducerSuppliedSignatures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arrange func(t *testing.T, dist *producerDist)
		message string // empty: admitted
	}{
		{name: "unsigned", arrange: func(*testing.T, *producerDist) {}},
		{
			// Not the signature of a declared file, so a checksum subject
			// like any other asset.
			name: "asset that only looks like a signature",
			arrange: func(t *testing.T, dist *producerDist) {
				t.Helper()
				dist.file(t, "notes.sig", true)
			},
		},
		{
			name: "undeclared signature of an asset",
			arrange: func(t *testing.T, dist *producerDist) {
				t.Helper()
				dist.plant(t, "app.tgz.sig")
			},
			message: "unsigned dist must not hold dist/app.tgz.sig",
		},
		{
			name: "undeclared bundle of the checksums file",
			arrange: func(t *testing.T, dist *producerDist) {
				t.Helper()
				dist.plant(t, "SHA256SUMS.bundle")
			},
			message: "unsigned dist must not hold dist/SHA256SUMS.bundle",
		},
		{
			name: "declared signature of an asset",
			arrange: func(t *testing.T, dist *producerDist) {
				t.Helper()
				dist.file(t, "app.tgz.bundle", false)
			},
			message: "must not declare a signature: dist/app.tgz.bundle",
		},
		{
			name: "declared provenance statement",
			arrange: func(t *testing.T, dist *producerDist) {
				t.Helper()
				dist.file(t, "slsa-provenance.intoto.json", false)
				dist.provenance = []string{"slsa-provenance.intoto.json"}
			},
			message: "must not declare provenance",
		},
		{
			name: "undeclared provenance statement",
			arrange: func(t *testing.T, dist *producerDist) {
				t.Helper()
				dist.plant(t, "slsa-provenance.intoto.json")
			},
			message: "unsigned dist must not hold dist/slsa-provenance.intoto.json",
		},
		{
			name: "undeclared provenance bundle",
			arrange: func(t *testing.T, dist *producerDist) {
				t.Helper()
				dist.plant(t, "slsa-provenance.intoto.json.bundle")
			},
			message: "unsigned dist must not hold dist/slsa-provenance.intoto.json.bundle",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dist := newProducerDist(t)
			tc.arrange(t, dist)
			dist.write(t)

			err := runReleaseFilesRoot(t, "validate", "--unsigned")
			if tc.message == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, errs.ErrValidation)
			require.ErrorContains(t, err, tc.message)
		})
	}
}

// Only a default manifest path that does not exist selects GoReleaser
// discovery. A manifest named by the flag or its variable has to exist, and
// one that exists but cannot be used is an error, never "no manifest".
func TestReleaseFilesRoot_OnlyAnAbsentDefaultManifestSelectsDiscovery(t *testing.T) {
	const discovered = "expected exactly one checksums file in dist, found 0"

	for _, tc := range []struct {
		name    string
		arrange func(t *testing.T, env *testenv.Env)
		args    []string
		want    error
		message string
	}{
		{name: "default absent", want: errs.ErrValidation, message: discovered},
		{
			name: "empty manifest variable",
			arrange: func(t *testing.T, _ *testenv.Env) {
				t.Helper()
				t.Setenv("RELEASE_FILES_MANIFEST", "")
			},
			want: errs.ErrValidation, message: discovered,
		},
		{
			name: "named by flag and absent", args: []string{"--manifest", "dist/release-files.json"},
			want: errs.ErrMissingInput, message: "release file manifest does not exist: dist/release-files.json",
		},
		{
			name: "named by variable and absent",
			arrange: func(t *testing.T, _ *testenv.Env) {
				t.Helper()
				t.Setenv("RELEASE_FILES_MANIFEST", "dist/producer.json")
			},
			want: errs.ErrMissingInput, message: "release file manifest does not exist: dist/producer.json",
		},
		{
			name: "default is a directory",
			arrange: func(t *testing.T, env *testenv.Env) {
				t.Helper()
				env.MkdirAll("dist/release-files.json")
			},
			want: errs.ErrMissingInput, message: "release file manifest dist/release-files.json: artifact path is not a regular file",
		},
		{
			// A link is refused as a link, with the class every other linked
			// release file gets, not reported as a file that is missing.
			name: "default is a dangling link",
			arrange: func(t *testing.T, env *testenv.Env) {
				t.Helper()
				require.NoError(t, os.Symlink("gone.json", env.Path("dist/release-files.json")))
			},
			want: errs.ErrValidation, message: "release file manifest dist/release-files.json: artifact path contains symlink component",
		},
	} {
		for _, verb := range [][]string{{"checksum-file"}, {"list", "--section", "checksums"}, {"validate-checksums"}} {
			t.Run(tc.name+"/"+verb[0], func(t *testing.T) {
				env := provenanceRootEnv(t, "forgejo")
				env.MkdirAll("dist")
				require.NoError(t, os.WriteFile(env.Path("dist/artifacts.json"), []byte("[]"), 0o600))

				if tc.arrange != nil {
					tc.arrange(t, env)
				}

				err := runReleaseFilesRoot(t, append(verb, tc.args...)...)
				require.ErrorIs(t, err, tc.want)
				require.ErrorContains(t, err, tc.message)
			})
		}
	}
}

// A finalization pointed at a manifest that is not there refuses instead of
// writing a fresh one from GoReleaser metadata.
func TestReleaseFilesRoot_ManifestDoesNotReplaceAMissingNamedManifest(t *testing.T) {
	env := provenanceRootEnv(t, "forgejo")
	env.MkdirAll("dist")

	err := runReleaseFilesRoot(t, "manifest", "--manifest", "dist/release-files.json")
	require.ErrorIs(t, err, errs.ErrMissingInput)
	require.ErrorContains(t, err, "release file manifest does not exist")
	require.NoFileExists(t, env.Path("dist/release-files.json"))
}
