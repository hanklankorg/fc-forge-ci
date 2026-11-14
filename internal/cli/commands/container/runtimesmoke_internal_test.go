// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
)

func TestRuntimeSmokeCommandForwardsAllExpectedIdentities(t *testing.T) {
	t.Parallel()

	expected := domaincontainer.RuntimeSmokeReceipt{Source: strings.Repeat("a", 40), Target: "go", Arch: "amd64", Ref: "registry.example/owner/runtime@sha256:" + strings.Repeat("b", 64)}

	body, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(t.TempDir(), "receipt with spaces.json")
	if err := os.WriteFile(file, body, 0o600); err != nil {
		t.Fatal(err)
	}

	args := []string{"verify-smoke", "--receipt", file, "--source-sha", expected.Source, "--target", expected.Target, "--arch", expected.Arch, "--image-ref", expected.Ref}
	if err := runtimePublicationVerifySmokeCmd().Run(t.Context(), args); err != nil {
		t.Fatal(err)
	}

	args[len(args)-1] = "registry.example/owner/runtime@sha256:" + strings.Repeat("c", 64)
	if err := runtimePublicationVerifySmokeCmd().Run(t.Context(), args); err == nil {
		t.Fatal("substituted digest passed CLI verification")
	}
}
