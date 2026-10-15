// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package security_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appsecurity "github.com/diggsweden/forge-ci/internal/app/security"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/stretchr/testify/require"
)

func reportFlag(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}

	return ""
}

func TestContainerFreshness_NoOldEvidenceOrPartialPublication(t *testing.T) { //nolint:gocognit // independent process/data/type outcomes share the same owned publication fixture.
	t.Parallel()

	cause := errors.New("owned scanner failure") //nolint:err113 // independent tool cause.

	for _, mode := range []string{"no-write", "empty", "malformed", "empty-object", "unknown-only", "status", "tool-error", "link", "oversized", "clean", "clean-empty-array", "clean-named-omitted", "clean-named-null", "findings"} {
		t.Run(mode, func(t *testing.T) {
			clean := strings.HasPrefix(mode, "clean")
			root := t.TempDir()

			raw, sarif, gitlab := filepath.Join(root, "raw.json"), filepath.Join(root, "out.sarif"), filepath.Join(root, "gitlab.json")
			for _, path := range []string{raw, sarif, gitlab} {
				require.NoError(t, os.WriteFile(path, []byte(`{"Results":[],"old":"OLD-CANARY"}`), 0600))
			}

			canary := filepath.Join(t.TempDir(), "canary")
			require.NoError(t, os.WriteFile(canary, []byte("outside unchanged"), 0600))

			var staged string

			scanner := &scanRecorder{run: func(_ context.Context, args []string) (int, error) {
				staged = scanOutput(args)
				require.NotEqual(t, raw, staged)
				require.True(t, filepath.IsAbs(staged))
				require.NoFileExists(t, staged)

				for _, path := range []string{raw, sarif, gitlab} {
					require.NoFileExists(t, path)
				}

				body := `{"ArtifactName":"current","Results":[]}`

				switch mode {
				case "no-write":
					return 0, nil
				case "tool-error":
					return 0, cause
				case "empty":
					body = ""
				case "malformed":
					body = "{"
				case "empty-object":
					body = `{}`
				case "unknown-only":
					body = `{"unexpected":true}`
				case "clean-empty-array":
					body = `{"Results":[]}`
				case "clean-named-omitted":
					body = `{"ArtifactName":"current"}`
				case "clean-named-null":
					body = `{"ArtifactName":"current","Results":null}`
				case "link":
					return 0, os.Symlink(canary, staged)
				case "oversized":
					f, err := os.Create(staged)
					require.NoError(t, err)
					require.NoError(t, f.Truncate((64<<20)+1))
					require.NoError(t, f.Close())

					return 0, nil
				case "findings":
					body = boundaryTrivyJSON
				}

				require.NoError(t, os.WriteFile(staged, []byte(body), 0600))

				if mode == "status" {
					return 7, nil
				}

				return 0, nil
			}}

			var out bytes.Buffer

			err := appsecurity.ScanContainer(t.Context(), scanner, &out, &out, output.Annotator{}, appsecurity.ScanContainerInput{ImageRef: "registry.invalid/owner/image:tag", JSONFile: raw, SARIFFile: sarif, GitLabReportFile: gitlab})
			want := errs.ErrMalformedInput

			switch mode {
			case "no-write":
				want = errs.ErrMissingInput
			case "status":
				want = errs.ErrDependencyUnavailable
			case "tool-error":
				want = cause
			case "clean", "clean-empty-array", "clean-named-omitted", "clean-named-null":
				want = nil
			case "findings":
				want = errs.ErrValidation
			}

			require.ErrorIs(t, err, want)
			require.Len(t, scanner.calls, 1)

			_, statErr := os.Stat(filepath.Dir(staged))
			require.ErrorIs(t, statErr, os.ErrNotExist)

			body, readErr := os.ReadFile(canary)
			require.NoError(t, readErr)
			require.Equal(t, "outside unchanged", string(body))

			for _, path := range []string{raw, sarif, gitlab} {
				if mode == "findings" || clean && path == raw {
					body, readErr := os.ReadFile(path)
					require.NoError(t, readErr)
					require.True(t, json.Valid(body))
					require.NotContains(t, string(body), "OLD-CANARY")
				} else {
					require.NoFileExists(t, path)
				}
			}

			if clean {
				require.Contains(t, out.String(), "No vulnerabilities found")
			} else {
				require.NotContains(t, out.String(), "No vulnerabilities found")
			}
		})
	}
}

func TestScanFreshness_InvalidPathsPreservePreviousReports(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"alias", "trailing-separator"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			raw := filepath.Join(root, "raw.json")
			require.NoError(t, os.WriteFile(raw, []byte("OLD-CANARY"), 0600))

			other := filepath.Join(root, "report.sarif")
			require.NoError(t, os.WriteFile(other, []byte("OTHER-CANARY"), 0o600))

			report := raw
			if mode == "trailing-separator" {
				report = other + string(filepath.Separator)
			}

			scanner := &scanRecorder{run: func(context.Context, []string) (int, error) {
				t.Error("scanner called")

				return 0, nil
			}}
			err := appsecurity.ScanContainer(t.Context(), scanner, io.Discard, io.Discard, output.Annotator{}, appsecurity.ScanContainerInput{ImageRef: "image", JSONFile: raw, SARIFFile: report, GitLabReportFile: filepath.Join(root, "g")})
			require.ErrorIs(t, err, errs.ErrUsage)
			require.Empty(t, scanner.calls)

			body, err := os.ReadFile(raw)
			require.NoError(t, err)
			require.Equal(t, "OLD-CANARY", string(body))
			body, err = os.ReadFile(other)
			require.NoError(t, err)
			require.Equal(t, "OTHER-CANARY", string(body))
		})
	}
}

func TestScanFreshness_UnsupportedSeverityPreservesReports(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	raw := filepath.Join(root, "raw.json")
	require.NoError(t, os.WriteFile(raw, []byte("OLD-CANARY"), 0600))

	scanner := &scanRecorder{run: func(context.Context, []string) (int, error) {
		t.Error("scanner called")

		return 0, nil
	}}

	for _, severity := range []string{"INVALID", "HIGH,", "HIGH\nLOW"} {
		err := appsecurity.ScanContainer(t.Context(), scanner, io.Discard, io.Discard, output.Annotator{}, appsecurity.ScanContainerInput{ImageRef: "image", Severity: severity, JSONFile: raw, SARIFFile: filepath.Join(root, "s"), GitLabReportFile: filepath.Join(root, "g")})
		require.ErrorIs(t, err, errs.ErrUsage)
	}

	require.Empty(t, scanner.calls)

	body, readErr := os.ReadFile(raw)
	require.NoError(t, readErr)
	require.Equal(t, "OLD-CANARY", string(body))
}

func TestScanFreshness_SupportedSeverityIsNormalised(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	scanner := &scanRecorder{run: func(_ context.Context, args []string) (int, error) {
		require.Equal(t, "UNKNOWN,LOW,MEDIUM,HIGH,CRITICAL", reportFlag(args, "--severity"))

		return 0, os.WriteFile(scanOutput(args), []byte(`{"ArtifactName":"clean-native-report"}`), 0600)
	}}
	require.NoError(t, appsecurity.ScanContainer(t.Context(), scanner, io.Discard, io.Discard, output.Annotator{}, appsecurity.ScanContainerInput{ImageRef: "image", Severity: "unknown, low,medium,HIGH,critical", JSONFile: filepath.Join(root, "raw.json"), SARIFFile: filepath.Join(root, "sarif"), GitLabReportFile: filepath.Join(root, "gitlab")}))
	require.Len(t, scanner.calls, 1)
}
