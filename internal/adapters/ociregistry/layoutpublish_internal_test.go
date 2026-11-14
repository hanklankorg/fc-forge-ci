// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ociregistry

import (
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestPushIndexLayout_PreservesTheExportedIndex(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(server.Close)
	index, _ := platformIndex(t)
	raw, err := index.RawManifest()
	require.NoError(t, err)
	directory := t.TempDir()
	root, err := layout.Write(directory, empty.Index)
	require.NoError(t, err)
	require.NoError(t, root.AppendIndex(index))

	reference := strings.TrimPrefix(server.URL, "http://") + "/owner/image:v1"
	digest, err := NewAnonymous().PushIndexLayout(t.Context(), directory, reference)
	require.NoError(t, err)
	served, err := NewAnonymous().Manifest(t.Context(), reference)
	require.NoError(t, err)
	require.Equal(t, raw, served)
	require.Equal(t, fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), digest)
}

func TestPushIndexLayout_RejectsAmbiguityAndDigestMismatchBeforeRequests(t *testing.T) {
	t.Parallel()

	for _, count := range []int{0, 1, 2} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			t.Parallel()
			rec := newRecordingRegistry(t)
			directory := t.TempDir()
			root, err := layout.Write(directory, empty.Index)
			require.NoError(t, err)

			for range count {
				index, _ := platformIndex(t)
				require.NoError(t, root.AppendIndex(index))
			}

			_, err = NewAnonymous().PushIndexLayout(t.Context(), directory, rec.repo+"@sha256:"+strings.Repeat("0", 64))
			if count == 1 {
				require.ErrorIs(t, err, errs.ErrValidation)
			} else {
				require.ErrorIs(t, err, errs.ErrMalformedInput)
			}

			require.Zero(t, rec.count())
		})
	}
}

func TestPublicationTLSVerificationIsExplicit(t *testing.T) {
	t.Parallel()

	server := httptest.NewUnstartedServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	t.Cleanup(server.Close)
	directory := t.TempDir()
	root, err := layout.Write(directory, empty.Index)
	require.NoError(t, err)
	require.NoError(t, root.AppendImage(empty.Image))

	reference := strings.TrimPrefix(server.URL, "https://") + "/owner/image:v1"
	_, err = WithAuthFileAndTLS("", true).PushLayout(t.Context(), directory, reference)
	require.Error(t, err, "an untrusted TLS certificate must not be accepted by default")

	publisher := WithAuthFileAndTLS("", false)
	digest, err := publisher.PushLayout(t.Context(), directory, reference)
	require.NoError(t, err)
	_, err = WithAuthFileAndTLS("", true).Manifest(t.Context(), reference)
	require.Error(t, err, "read-back must keep explicit TLS verification too")
	raw, err := publisher.Manifest(t.Context(), reference)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), digest)
}

func TestExplicitTLSRefusesPlainHTTPPublication(t *testing.T) {
	t.Parallel()
	rec := newRecordingRegistry(t)
	directory := t.TempDir()
	root, err := layout.Write(directory, empty.Index)
	require.NoError(t, err)
	require.NoError(t, root.AppendImage(empty.Image))
	_, err = WithAuthFileAndTLS("", true).PushLayout(t.Context(), directory, rec.repo+":v1")
	require.Error(t, err)
	require.Empty(t, rec.writes())
}
