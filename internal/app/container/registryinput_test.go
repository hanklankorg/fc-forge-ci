// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/stretchr/testify/require"
)

type registryInputFixture struct {
	manifest []byte
	image    domaincontainer.LayoutImage
	copies   int
	loads    int
	// local, when set, is the name the builder answers with.
	local string
}

// LoadRegistryInput names the input as Buildah does, after the digest it is given.
func (f *registryInputFixture) LoadRegistryInput(_ context.Context, _, digest string, _ io.Writer) (string, error) {
	f.loads++
	if f.local != "" {
		return f.local, nil
	}

	return "localhost/forge-ci-input:" + strings.TrimPrefix(digest, "sha256:"), nil
}

func (f *registryInputFixture) Manifest(context.Context, string) ([]byte, error) {
	return f.manifest, nil
}
func (f *registryInputFixture) InspectLayout(context.Context, string) (domaincontainer.LayoutImage, error) {
	return f.image, nil
}
func (f *registryInputFixture) CopyDockerDigestToOCILayout(_ context.Context, _, _, directory, _, _ string) error {
	f.copies++

	return os.Mkdir(directory, 0o700)
}

func TestRegistryInputIsBoundToPinnedSourceAndSelectedPlatform(t *testing.T) {
	child := "sha256:" + strings.Repeat("a", 64)
	manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"manifests":[{"digest":%q,"platform":{"os":"linux","architecture":"amd64"}}]}`, child))
	hash := sha256.Sum256(manifest)
	ref := fmt.Sprintf("registry.example/owner/private@sha256:%x", hash)
	fixture := &registryInputFixture{manifest: manifest, image: domaincontainer.LayoutImage{Digest: child, Platform: "linux/amd64"}}
	directory := filepath.Join(t.TempDir(), "parent")
	require.NoError(t, appcontainer.FetchRegistryInput(t.Context(), fixture, ref, "linux/amd64", directory))
	require.Equal(t, 1, fixture.copies)
	_, err := appcontainer.VerifyRegistryInput(t.Context(), fixture, ref, "linux/amd64", directory)
	require.NoError(t, err)
	local, err := appcontainer.ImportRegistryInput(t.Context(), fixture, fixture, io.Discard, ref, "linux/amd64", directory)
	require.NoError(t, err)
	require.Equal(t, "localhost/forge-ci-input:"+strings.Repeat("a", 64), local)
	require.Equal(t, 1, fixture.loads)
	fixture.image.Platform = "linux/arm64"
	_, err = appcontainer.VerifyRegistryInput(t.Context(), fixture, ref, "linux/amd64", directory)
	require.ErrorContains(t, err, "platform differs")

	fixture.image.Platform = "linux/amd64"
	fixture.image.Digest = "sha256:" + strings.Repeat("b", 64)
	_, err = appcontainer.VerifyRegistryInput(t.Context(), fixture, ref, "linux/amd64", directory)
	require.ErrorContains(t, err, "not a selected child")
	_, err = appcontainer.ImportRegistryInput(t.Context(), fixture, fixture, io.Discard, ref, "linux/amd64", directory)
	require.Error(t, err)
	require.Equal(t, 1, fixture.loads, "unverified bytes must never reach the builder")
	require.NoError(t, os.WriteFile(filepath.Join(directory, "forge-ci-source-manifest.json"), []byte(`{}`), 0o600))
	_, err = appcontainer.VerifyRegistryInput(t.Context(), fixture, ref, "linux/amd64", directory)
	require.ErrorContains(t, err, "differs from pinned digest")
	require.Equal(t, 1, fixture.copies, "verification must never fetch or copy")
}

func TestRegistryInputRefusesMutableReferencesAndInvalidSourcesBeforeCopy(t *testing.T) {
	fixture := &registryInputFixture{manifest: []byte(`{}`)}
	for _, ref := range []string{"registry.example/owner/private:latest", "registry.example/owner/private@sha256:" + strings.Repeat("a", 64)} {
		err := appcontainer.FetchRegistryInput(t.Context(), fixture, ref, "linux/amd64", filepath.Join(t.TempDir(), "parent"))
		require.Error(t, err)
	}

	require.Zero(t, fixture.copies)
}

// A build argument is rewritten to the name the builder loaded the parent
// under, so that name must be the local one, by the verified digest: Buildah
// tags it, kaniko pins it. Any other name could be served by a registry.
func TestRegistryInputRefusesANameOutsideTheLocalRepository(t *testing.T) {
	child := "sha256:" + strings.Repeat("a", 64)
	manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"manifests":[{"digest":%q,"platform":{"os":"linux","architecture":"amd64"}}]}`, child))
	ref := fmt.Sprintf("registry.example/owner/private@sha256:%x", sha256.Sum256(manifest))
	directory := filepath.Join(t.TempDir(), "parent")

	for local, admitted := range map[string]bool{
		"localhost/forge-ci-input@" + child:                          true,
		"localhost/forge-ci-input:" + strings.Repeat("a", 64):        true,
		"registry.example/owner/private@" + child:                    false,
		"localhost/forge-ci-input-other@" + child:                    false,
		"localhost/forge-ci-input:" + strings.Repeat("b", 64):        false,
		"localhost/forge-ci-input@sha256:" + strings.Repeat("b", 64): false,
	} {
		fixture := &registryInputFixture{manifest: manifest, image: domaincontainer.LayoutImage{Digest: child, Platform: "linux/amd64"}, local: local}
		if _, err := os.Stat(directory); err != nil {
			require.NoError(t, appcontainer.FetchRegistryInput(t.Context(), fixture, ref, "linux/amd64", directory))
		}

		got, err := appcontainer.ImportRegistryInput(t.Context(), fixture, fixture, io.Discard, ref, "linux/amd64", directory)
		if admitted {
			require.NoError(t, err, local)
			require.Equal(t, local, got)
		} else {
			require.ErrorIs(t, err, errs.ErrValidation, local)
		}
	}
}
