// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/container"
)

func TestBuilderMaterialID_BindsBackendAndExecutable(t *testing.T) {
	t.Parallel()

	identity := container.BuilderIdentity{Kind: "buildah", SHA256: strings.Repeat("a", 64)}
	id := identity.MaterialID()
	require.True(t, container.ValidSHA256Hex(id))
	require.Equal(t, id, identity.MaterialID())

	identity.Kind = "kaniko"
	require.NotEqual(t, id, identity.MaterialID(), "backend changes invalidate the cache material")
	identity.Kind = "buildah"
	identity.SHA256 = strings.Repeat("b", 64)
	require.NotEqual(t, id, identity.MaterialID(), "executable changes invalidate the cache material")
}
