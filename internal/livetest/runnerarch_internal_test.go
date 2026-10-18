// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"runtime"
	"testing"

	"codefloe.com/itiquette/forge-lab-contract/labcontract"
)

func TestLabRunnerArch_IsTheOneArchitectureTheRunnersPublish(t *testing.T) {
	t.Parallel()

	runner := func(kind, arch string) labcontract.Endpoint {
		return labcontract.Endpoint{Name: kind, Kind: labcontract.Kind(kind), Capabilities: []string{capabilityWorkflowRuns}, RunnerArch: arch}
	}

	tests := []struct {
		name      string
		endpoints []labcontract.Endpoint
		want      string
		refused   bool
	}{
		{name: "agreeing runners", endpoints: []labcontract.Endpoint{runner("gitlab", "arm64"), runner("forgejo", "arm64")}, want: "arm64"},
		{name: "no runner", endpoints: []labcontract.Endpoint{{Name: "gitlab", Kind: "gitlab"}}, want: runtime.GOARCH},
		{name: "unpublished", endpoints: []labcontract.Endpoint{runner("gitlab", "")}, refused: true},
		{name: "differing runners", endpoints: []labcontract.Endpoint{runner("gitlab", "amd64"), runner("forgejo", "arm64")}, refused: true},
	}

	for _, test := range tests {
		got, err := labRunnerArch(&labcontract.Contract{Endpoints: test.endpoints})
		if test.refused != (err != nil) || got != test.want {
			t.Errorf("%s: labRunnerArch() = %q, %v", test.name, got, err)
		}
	}
}

func TestNativeReleasePlatforms_DefaultsToTheRunnerAndRefusesAnotherArchitecture(t *testing.T) {
	t.Parallel()

	got, err := NativeReleasePlatforms(map[string]string{"OTHER": "kept"}, "arm64")
	if err != nil || got[releasePlatformsVariable] != "linux/arm64" || got["OTHER"] != "kept" {
		t.Fatalf("an unset selection: %v, %v", got, err)
	}

	if got, err = NativeReleasePlatforms(map[string]string{releasePlatformsVariable: "linux/amd64"}, "amd64"); err != nil ||
		got[releasePlatformsVariable] != "linux/amd64" {
		t.Fatalf("a native selection: %v, %v", got, err)
	}

	for _, selection := range []string{"linux/arm64", "linux/amd64,linux/arm64"} {
		if _, err := NativeReleasePlatforms(map[string]string{releasePlatformsVariable: selection}, "amd64"); err == nil {
			t.Errorf("%s was accepted on an amd64 lab", selection)
		}
	}
}
