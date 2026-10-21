// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	apppublish "github.com/diggsweden/forge-ci/internal/app/publish"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

type npmTagProbe struct {
	response   string
	viewErr    error
	viewStderr string
	calls      [][]string
	credential string
}

func (p *npmTagProbe) RunInherit(_ context.Context, _ string, stdout, stderr io.Writer, args ...string) error {
	p.calls = append(p.calls, args)
	for i, arg := range args {
		if arg == "--userconfig" {
			p.credential = args[i+1]
		}
	}

	if args[0] == "view" {
		_, _ = io.WriteString(stdout, p.response)
		_, _ = io.WriteString(stderr, p.viewStderr)

		return p.viewErr
	}

	return nil
}

func TestNPMPublishChecksTagBeforeMutation(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, setting, response string
		wantCalls               int
		wantError               error
	}{
		{"missing tag", "", "{}", 0, errs.ErrInvalidConfig},
		{"latest", "latest", "{}", 0, errs.ErrInvalidConfig},
		{"next", "next", "{}", 0, errs.ErrInvalidConfig},
		{"wrong version", "release-9.0.0", "{}", 0, errs.ErrInvalidConfig},
		{"conflict", "release-{version}", `{"release-1.2.3-rc.1":"0.1.0"}`, 1, errs.ErrValidation},
		{"invalid response", "release-{version}", `not JSON`, 1, errs.ErrMalformedInput},
		{"new binding", "release-{version}", `{}`, 2, nil},
		{"same binding", "release-1.2.3-rc.1", `{"release-1.2.3-rc.1":"1.2.3-rc.1"}`, 2, nil},
		{"dotted wrong version", "release.9.0.0", "{}", 0, errs.ErrInvalidConfig},
		{"dotted conflict", "release.{version}", `{"release.1.2.3-rc.1":"0.1.0"}`, 1, errs.ErrValidation},
		{"dotted literal conflict", "release.1.2.3-rc.1", `{"release.1.2.3-rc.1":"0.1.0"}`, 1, errs.ErrValidation},
		{"dotted new binding", "release.{version}", `{}`, 2, nil},
		{"dotted same binding", "release.1.2.3-rc.1", `{"release.1.2.3-rc.1":"1.2.3-rc.1"}`, 2, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeNPMPackage(t, filepath.Join(dir, "misleading-9.0.0.tgz"), "@fixture/pkg", "1.2.3-rc.1")

			probe := &npmTagProbe{response: tc.response}
			resolver := fakeNPMRegistryResolver{reg: provider.ForgeNPMRegistry{Registry: "https://registry.example/", Token: "synthetic"}}
			err := apppublish.ForgePackagesNPMPublish(t.Context(), probe, resolver, io.Discard, io.Discard, apppublish.ForgePackagesNPMPublishInput{WorkingDir: dir, Tag: tc.setting})
			require.ErrorIs(t, err, tc.wantError)
			require.Len(t, probe.calls, tc.wantCalls)

			if tc.wantCalls == 2 {
				wantTag := strings.ReplaceAll(tc.setting, "{version}", "1.2.3-rc.1")
				require.Equal(t, []string{"publish", "./misleading-9.0.0.tgz", "--userconfig", probe.credential, "--registry", "https://registry.example/", "--tag", wantTag, "--ignore-scripts"}, probe.calls[1])
			}

			if probe.credential != "" {
				_, err := os.Stat(probe.credential)
				require.ErrorIs(t, err, os.ErrNotExist)
			}
		})
	}
}

func TestNPMPublishDoesNotMistakeRegistryFailureForAbsence(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeNPMPackage(t, filepath.Join(dir, "pkg.tgz"), "pkg", "1.2.3")

	unavailable := errs.ErrDependencyUnavailable
	probe := &npmTagProbe{viewErr: unavailable, viewStderr: "npm error code E503"}
	resolver := fakeNPMRegistryResolver{reg: provider.ForgeNPMRegistry{Registry: "https://registry.example/", Token: "synthetic"}}
	err := apppublish.ForgePackagesNPMPublish(t.Context(), probe, resolver, io.Discard, io.Discard, apppublish.ForgePackagesNPMPublishInput{WorkingDir: dir, Tag: "release-{version}"})
	require.ErrorIs(t, err, unavailable)
	require.Len(t, probe.calls, 1)
}
