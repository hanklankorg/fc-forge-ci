// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package livetest

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

func TestForgejoNpmExactCaseCleanup(t *testing.T) {
	RegisterLiveScenario(t)

	input := os.Getenv("FORGE_CI_LIVE_NPM_CLEANUP_ACK")
	if input == "" {
		t.Skip("explicit exact case package acknowledgement required")
	}

	var ack struct {
		Repository       string `json:"Repository"`
		Name             string `json:"Name"`
		Version          string `json:"Version"`
		AbsentBeforeCase bool   `json:"AbsentBeforeCase"`
	}
	if e := json.Unmarshal([]byte(input), &ack); e != nil || ack.Name == "" || ack.Version == "" || !ack.AbsentBeforeCase {
		t.Fatal("exact new-case version acknowledgement required")
	}

	target := Accept(t, provider.ForgeForgejo)
	if !inRunNamespace(target, ack.Repository) {
		t.Fatal("repository outside admitted scratch namespace")
	}

	packages, e := publishedPackages(target, ack.Repository, "npm", ack.Name, packageRequests(Context(t), target))
	if e != nil {
		t.Fatal("exact npm lookup failed; no absence claim")
	}

	for _, pkg := range packages {
		if pkg.Version == ack.Version {
			t.Logf("new case npm version observed: id=%d name=%s version=%s", pkg.ID, pkg.Name, pkg.Version)
		}
	}

	DeletePublishedPackage(t, target, ack.Repository, "npm", ack.Name, ack.Version)

	packages, e = publishedPackages(target, ack.Repository, "npm", ack.Name, packageRequests(Context(t), target))
	if e != nil {
		t.Fatal("npm cleanup postcondition unreadable")
	}

	for _, pkg := range packages {
		if pkg.Version == ack.Version {
			t.Fatal("exact npm version remains")
		}
	}

	t.Logf("exact case npm version absent: name=%s version=%s", ack.Name, ack.Version)
}
