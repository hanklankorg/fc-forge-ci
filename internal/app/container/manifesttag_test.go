// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/stretchr/testify/require"
)

type manifestTagStore struct {
	copies  [][2]string
	err     error
	reads   []string
	digests map[string]string
	readErr error
}

func (s *manifestTagStore) ResolveDigest(_ context.Context, ref string) (string, error) {
	s.reads = append(s.reads, ref)
	if digest, ok := s.digests[ref]; ok {
		return digest, s.readErr
	}

	return "sha256:" + strings.Repeat("a", 64), s.readErr
}

func (s *manifestTagStore) CopyTag(_ context.Context, source, dest string) error {
	s.copies = append(s.copies, [2]string{source, dest})

	return s.err
}

func TestTagManifestPreflightsEveryAlias(t *testing.T) {
	t.Parallel()

	const image = "ghcr.io/owner/app"

	source := image + "@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct{ source, tags string }{
		{image + ":candidate", image + ":latest"},
		{source, ""},
		{source, image + ":latest\nghcr.io/other/app:v1"},
		{source, image + ":latest\n" + image + ":latest"},
		{image + ":candidate@sha256:" + strings.Repeat("a", 64), image + ":latest"},
	} {
		store := &manifestTagStore{}
		require.Error(t, appcontainer.TagManifest(t.Context(), store, tc.source, tc.tags))
		require.Empty(t, store.copies)
		require.Empty(t, store.reads)
	}

	store := &manifestTagStore{}
	require.NoError(t, appcontainer.TagManifest(t.Context(), store, source, image+":v1\n\n"+image+":latest\n"))
	require.Equal(t, [][2]string{{source, image + ":v1"}, {source, image + ":latest"}}, store.copies)
	require.Equal(t, []string{image + ":v1", image + ":latest"}, store.reads)

	failure := errors.New("copy refused")
	store = &manifestTagStore{err: failure}
	require.ErrorIs(t, appcontainer.TagManifest(t.Context(), store, source, image+":v1\n"+image+":latest"), failure)
	require.Len(t, store.copies, 1)
	require.Empty(t, store.reads)
}

func TestTagManifestRejectsOverlappingAliasPublication(t *testing.T) {
	t.Parallel()

	const image = "ghcr.io/owner/app"

	source := image + "@sha256:" + strings.Repeat("a", 64)

	aliases := []string{image + ":v1", image + ":latest", image + ":staging-v1"}
	for _, moved := range aliases {
		t.Run(moved, func(t *testing.T) {
			t.Parallel()

			store := &manifestTagStore{digests: map[string]string{moved: "sha256:" + strings.Repeat("b", 64)}}
			err := appcontainer.TagManifest(t.Context(), store, source, strings.Join(aliases, "\n"))
			require.ErrorContains(t, err, "digest mismatch")
			require.ErrorContains(t, err, moved)
			require.Len(t, store.copies, len(aliases), "read-back follows the complete copy set")
			require.Contains(t, store.reads, moved)
		})
	}

	store := &manifestTagStore{}
	require.NoError(t, appcontainer.TagManifest(t.Context(), store, source, strings.Join(aliases, "\n")))
	require.Equal(t, aliases, store.reads, "every destination, including the staging alias, must be verified")

	failure := errors.New("alias read refused")
	store = &manifestTagStore{readErr: failure}
	require.ErrorIs(t, appcontainer.TagManifest(t.Context(), store, source, strings.Join(aliases, "\n")), failure)
}
