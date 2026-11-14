// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func sourceImagePhase(phase, acknowledgement string) (string, error) {
	switch phase {
	case "", "run":
		if acknowledgement != "" {
			return "", errors.New("retention acknowledgement requires explicit phase")
		}

		return "run", nil
	case "publish-retain", "resume", "cleanup":
		if acknowledgement != "retain-exact-package-until-public-confirmation" {
			return "", errors.New("explicit exact-package retention acknowledgement required")
		}

		return phase, nil
	default:
		return "", errors.New("unknown source image phase")
	}
}

func sourceImageFlavor(flavor string) (string, error) {
	if flavor == "" {
		flavor = "rust"
	}

	if flavor != "rust" && flavor != "go" {
		return "", errors.New("source image flavor must be rust or go")
	}

	return flavor, nil
}

func sourceImageBatchRetention(phase, acknowledgement string) (bool, error) {
	if acknowledgement == "" {
		return false, nil
	}

	if phase != "resume" || acknowledgement != "retain-exact-restored-package-until-batch-end" {
		return false, errors.New("batch retention requires resume and explicit exact-package acknowledgement")
	}

	return true, nil
}

func TestSourceImageFlavorAdmission(t *testing.T) {
	for input, want := range map[string]string{"": "rust", "rust": "rust", "go": "go"} {
		if got, err := sourceImageFlavor(input); err != nil || got != want {
			t.Fatalf("flavor %q: got %q, %v; want %q", input, got, err, want)
		}
	}

	for _, bad := range []string{"core", "java", "GO", "go/rust", "../go"} {
		if _, err := sourceImageFlavor(bad); err == nil {
			t.Fatalf("unsupported flavor %q accepted", bad)
		}
	}
}

func TestSourceImageBatchRetentionRequiresResumeAcknowledgement(t *testing.T) {
	const ack = "retain-exact-restored-package-until-batch-end"

	for _, phase := range []string{"run", "publish-retain", "resume", "cleanup"} {
		if retain, err := sourceImageBatchRetention(phase, ""); err != nil || retain {
			t.Fatal("default cleanup changed")
		}

		retain, err := sourceImageBatchRetention(phase, ack)
		if (phase == "resume") != (err == nil && retain) {
			t.Fatalf("unexpected batch retention admission for %s", phase)
		}
	}

	if _, err := sourceImageBatchRetention("resume", "true"); err == nil {
		t.Fatal("implicit retention accepted")
	}
}

func sourceImageSettingsURL(organization, packageName, provided string) (string, error) {
	if provided == "" {
		provided = "https://github.com/orgs/" + organization + "/packages/container/package/" + url.PathEscape(packageName)
	}

	u, err := url.Parse(provided)

	base := "/orgs/" + organization + "/packages/container/"
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != base+packageName && u.Path != base+"package/"+packageName) {
		return "", errors.New("package settings URL does not match exact admitted coordinate")
	}

	return strings.TrimSuffix(provided, "/") + "/settings", nil
}

func TestSourceImagePhaseRequiresRetentionAcknowledgement(t *testing.T) {
	for _, phase := range []string{"publish-retain", "resume", "cleanup"} {
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
