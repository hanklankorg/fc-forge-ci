// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func sourceImagePhase(phase, acknowledgement string) (string, error) {
	switch phase {
	case "", "run":
		if acknowledgement != "" {
			return "", fmt.Errorf("retention acknowledgement requires explicit phase")
		}
		return "run", nil
	case "publish-retain", "resume":
		if acknowledgement != "retain-exact-package-until-public-confirmation" {
			return "", fmt.Errorf("explicit exact-package retention acknowledgement required")
		}
		return phase, nil
	default:
		return "", fmt.Errorf("unknown source image phase")
	}
}

func sourceImageSettingsURL(organization, packageName, provided string) (string, error) {
	if provided == "" {
		provided = "https://github.com/orgs/" + organization + "/packages/container/package/" + url.PathEscape(packageName)
	}
	u, err := url.Parse(provided)
	base := "/orgs/" + organization + "/packages/container/"
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != base+packageName && u.Path != base+"package/"+packageName) {
		return "", fmt.Errorf("package settings URL does not match exact admitted coordinate")
	}
	return strings.TrimSuffix(provided, "/") + "/settings", nil
}

func TestSourceImagePhaseRequiresRetentionAcknowledgement(t *testing.T) {
	for _, phase := range []string{"publish-retain", "resume"} {
		if _, err := sourceImagePhase(phase, ""); err == nil {
			t.Fatal("implicit retention accepted")
		}
		if got, err := sourceImagePhase(phase, "retain-exact-package-until-public-confirmation"); err != nil || got != phase {
			t.Fatal("explicit phase refused")
		}
	}
	if _, err := sourceImagePhase("unknown", ""); err == nil {
		t.Fatal("unknown phase accepted")
	}
	if got, err := sourceImagePhase("", ""); err != nil || got != "run" {
		t.Fatal("ordinary cleanup default changed")
	}
}

func TestSourceImageSettingsURLUsesExactNestedCoordinate(t *testing.T) {
	if got, err := sourceImageSettingsURL("fixture-org", "fixture-engine/tool", "https://github.com/orgs/fixture-org/packages/container/package/fixture-engine%2Ftool"); err != nil || !strings.Contains(got, "/container/package/") {
		t.Fatal("actual package UI route refused")
	}
	got, err := sourceImageSettingsURL("fixture-org", "fixture-engine/tool", "https://github.com/orgs/fixture-org/packages/container/fixture-engine%2Ftool")
	if err != nil || !strings.HasSuffix(got, "fixture-engine%2Ftool/settings") {
		t.Fatal("nested package URL changed")
	}
	for _, bad := range []string{"https://example.invalid/orgs/fixture-org/packages/container/fixture-engine%2Ftool", "https://github.com/orgs/another/packages/container/fixture-engine%2Ftool", "https://github.com/orgs/fixture-org/packages/container/other"} {
		if _, err := sourceImageSettingsURL("fixture-org", "fixture-engine/tool", bad); err == nil {
			t.Fatal("foreign package settings URL accepted")
		}
	}
}
