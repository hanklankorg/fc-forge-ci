// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/stretchr/testify/require"
)

func TestRuntimePromotionRequiresTheReviewedKeyBeforeProviderEffects(t *testing.T) {
	t.Chdir(t.TempDir())

	key := []byte("public key fixture")
	require.NoError(t, os.WriteFile("cosign.pub", key, 0o600))

	for _, digest := range []string{"", "not-a-digest", strings.Repeat("0", 64)} {
		command := runtimePublicationPromoteCmd()
		err := command.Run(t.Context(), []string{"promote", "--cosign-public-key-path", "cosign.pub", "--cosign-public-key-sha256", digest})
		require.ErrorIs(t, err, errs.ErrValidation)
		require.NoFileExists(t, "published-ref.txt")
	}

	// A pin for one public key must not admit a substituted file.
	expected := fmt.Sprintf("%x", sha256.Sum256(key))

	require.NoError(t, os.WriteFile("cosign.pub", []byte("other key"), 0o600))
	err := runtimePublicationPromoteCmd().Run(t.Context(), []string{"promote", "--cosign-public-key-path", "cosign.pub", "--cosign-public-key-sha256", expected})
	require.ErrorIs(t, err, errs.ErrValidation)
	require.NoFileExists(t, "published-ref.txt")
}
