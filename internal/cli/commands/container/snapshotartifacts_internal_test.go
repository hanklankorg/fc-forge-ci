// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"slices"
	"testing"

	appenforcement "github.com/diggsweden/forge-ci/internal/app/enforcement"
	appsnapshot "github.com/diggsweden/forge-ci/internal/app/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/domain/enforcement"
)

// A snapshot artifact build runs the engine again; the child applies the
// administrator settings its parent was given, ahead of the build command.
func TestSnapshotSelfBuildKeepsTheAdministratorSettings(t *testing.T) {
	t.Parallel()

	request := appsnapshot.ArtifactBuildRequest{Args: []string{"build", "go", "run"}}
	ctx := appenforcement.WithAdmin(t.Context(), enforcement.Admin{enforcement.ApplicationTests: "false"})

	if got, want := selfBuildArgs(ctx, request), []string{"--enforce-application-tests=false", "build", "go", "run"}; !slices.Equal(got, want) {
		t.Fatalf("child args %q, want %q", got, want)
	}

	if got := selfBuildArgs(t.Context(), request); !slices.Equal(got, request.Args) {
		t.Fatalf("child args without admin input %q", got)
	}
}
