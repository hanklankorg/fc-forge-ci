// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import "testing"

func TestMavenServiceDevelopmentVariablesPreserveAuthority(t *testing.T) {
	plan := map[string]string{"FORGE_CI_REPOSITORY": "fixtures/engine", "CI_RUNNER_LABEL": "runner"}
	got, err := MavenServiceDevelopmentVariables(plan, map[string]string{"FORGE_CI_REPOSITORY": "fixtures/engine", "SERVICE_IMAGE_REPOSITORY": "registry.invalid/fixtures/service"})
	if err != nil || len(got) != 3 || got["FORGE_CI_REPOSITORY"] != "fixtures/engine" {
		t.Fatal("same binding was not deduplicated")
	}
	got["CI_RUNNER_LABEL"] = "changed"
	if plan["CI_RUNNER_LABEL"] != "runner" {
		t.Fatal("mutated admitted plan")
	}
	if _, err := MavenServiceDevelopmentVariables(plan, map[string]string{"FORGE_CI_REPOSITORY": "other/engine"}); err == nil {
		t.Fatal("conflicting repository authority accepted")
	}
}
