// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package security_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	appsecurity "github.com/diggsweden/forge-ci/internal/app/security"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domainsecurity "github.com/diggsweden/forge-ci/internal/domain/security"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
)

// readGitLabReport decodes a written GitLab security report and fails the
// test if it carries no vulnerability, so the field assertions below index
// the slice only once there is something to index.
func readGitLabReport(t *testing.T, fsys *testfs.Real, name string) domainsecurity.GitLabReport {
	t.Helper()

	var report domainsecurity.GitLabReport
	if err := json.Unmarshal(fsys.ReadFile(name), &report); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}

	if len(report.Vulnerabilities) != 1 {
		t.Fatalf("%s carries %d vulnerabilities, want 1", name, len(report.Vulnerabilities))
	}

	return report
}

const sampleTrivyJSON = `{
  "Results": [{"Target": "package-lock.json", "Class": "lang-pkgs", "Vulnerabilities": [
    {"VulnerabilityID": "CVE-X", "PkgName": "p", "InstalledVersion": "1", "FixedVersion": "2", "Severity": "high"}
  ]}]
}`

func TestTrivyToGitLab_EpochPinsCompleteReports(t *testing.T) {
	const input = `{
		"ArtifactName":"registry.example/fallback:old",
		"Metadata":{"OS":{"Family":"alpine","Name":"3.21"}},
		"Results":[{"Target":"package-lock.json","Class":"lang-pkgs","Vulnerabilities":[{
			"VulnerabilityID":"CVE-X","PkgName":"p","InstalledVersion":"1","FixedVersion":"2",
			"Title":"Advisory title","Description":"Explicit description","Severity":"high",
			"PrimaryURL":"https://example.invalid/advisory",
			"References":["https://example.invalid/z","https://example.invalid/a","https://example.invalid/a"]
		}]}]
	}`

	for _, epoch := range []struct{ seconds, stamp string }{
		{"1700000000", "2023-11-14T22:13:20"},
		{"1700000123", "2023-11-14T22:15:23"},
	} {
		t.Run(epoch.seconds, func(t *testing.T) {
			t.Setenv("SOURCE_DATE_EPOCH", epoch.seconds)

			fsys := testfs.NewReal(t)
			in := fsys.WriteFile("trivy.json", []byte(input))
			fsys.WriteFile("unrelated", []byte("untouched sibling"))

			var previous []byte

			for _, name := range []string{"first.json", "second.json"} {
				out := fsys.WriteFile(name, []byte("previous "+name))
				count, err := appsecurity.TrivyToGitLabContainer(appsecurity.TransformInput{
					InputPath: in, OutputPath: out, TrivyVersion: "0.69.3-fixture", ImageRef: "registry.example/explicit:release",
				})
				require.NoError(t, err)
				require.Equal(t, 1, count)

				body := fsys.ReadFile(name)
				// Literal wire expectations are independent of production structs,
				// constants and UUID helpers; this is not a schema-conformance claim.
				want := fmt.Sprintf(`{
							"version":"15.2.1",
							"scan":{
								"scanner":{"id":"trivy","name":"Trivy","version":"0.69.3-fixture","vendor":{"name":"Aqua Security"}},
								"analyzer":{"id":"trivy","name":"Trivy","version":"0.69.3-fixture","vendor":{"name":"Aqua Security"}},
								"type":"container_scanning","start_time":%q,"end_time":%q,"status":"success"
							},
							"vulnerabilities":[{
								"id":"1abe2d90-2173-22b9-743e-28abe55bbe6f","name":"Advisory title","description":"Explicit description","severity":"High","solution":"Upgrade p to 2",
								"identifiers":[{"type":"cve","name":"CVE-X","value":"CVE-X","url":"https://example.invalid/advisory"}],
								"links":[{"url":"https://example.invalid/a"},{"url":"https://example.invalid/advisory"},{"url":"https://example.invalid/z"}],
								"location":{"image":"registry.example/explicit:release","operating_system":"alpine 3.21","dependency":{"package":{"name":"p"},"version":"1"}}
							}]
						}`, epoch.stamp, epoch.stamp)
				require.JSONEq(t, want, string(body))

				if previous != nil {
					require.Equal(t, previous, body, "repeated transform changed report bytes")
				}

				previous = body
			}

			require.True(t, bytes.Equal([]byte(input), fsys.ReadFile("trivy.json")), "transform changed input bytes")
			require.Equal(t, "untouched sibling", string(fsys.ReadFile("unrelated")))
		})
	}
}

// An epoch that is set but unusable must stop the transform, not fall back to
// the wall clock: the caller asked for a reproducible report.
func TestTrivyToGitLab_UnusableEpochIsRefusedAndKeepsThePreviousReport(t *testing.T) {
	for _, epoch := range []string{"yesterday", "9223372036854775807"} {
		t.Run(epoch, func(t *testing.T) {
			t.Setenv("SOURCE_DATE_EPOCH", epoch)

			fsys := testfs.NewReal(t)
			in := fsys.WriteFile("trivy.json", []byte(`{"SchemaVersion":2,"Results":[]}`))
			out := fsys.WriteFile("report.json", []byte("previous report"))

			_, err := appsecurity.TrivyToGitLabContainer(appsecurity.TransformInput{InputPath: in, OutputPath: out})
			require.ErrorIs(t, err, errs.ErrUsage)
			require.ErrorContains(t, err, "SOURCE_DATE_EPOCH")
			require.Equal(t, "previous report", string(fsys.ReadFile("report.json")))
		})
	}
}

func TestTrivyToGitLabContainer_RoundTrip(t *testing.T) {
	t.Parallel()
	fsys := testfs.NewReal(t)
	in := fsys.WriteFile("trivy.json", []byte(`{
		"ArtifactName": "img",
		"Metadata": {"OS": {"Family": "alpine", "Name": "3.19"}},
		"Results": [{"Target": "x", "Vulnerabilities": [{
			"VulnerabilityID": "CVE-Y", "PkgName": "musl", "InstalledVersion": "1.2.4",
			"FixedVersion": "1.2.5", "Severity": "CRITICAL"
		}]}]
	}`))
	out := fsys.Path("gl.json")

	count, err := appsecurity.TrivyToGitLabContainer(appsecurity.TransformInput{
		InputPath: in, OutputPath: out, ImageRef: "ghcr.io/owner/img@sha256:abc",
	})
	if err != nil {
		t.Fatalf("TrivyToGitLabContainer: %v", err)
	}

	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}

	report := readGitLabReport(t, fsys, "gl.json")
	if report.Scan.Type != "container_scanning" {
		t.Errorf("scan.type = %q, want container_scanning", report.Scan.Type)
	}

	vuln := report.Vulnerabilities[0]
	if vuln.Name != "CVE-Y" || vuln.Severity != "Critical" {
		t.Errorf("vulnerability mapping = %+v", vuln)
	}

	if vuln.Location.Image != "ghcr.io/owner/img@sha256:abc" || vuln.Location.OperatingSystem != "alpine 3.19" {
		t.Errorf("container location = %+v", vuln.Location)
	}

	if vuln.Location.Dependency.Package.Name != "musl" || vuln.Location.Dependency.Version != "1.2.4" {
		t.Errorf("dependency mapping = %+v", vuln.Location.Dependency)
	}
}

func TestTrivyToGitLab_ReadOnlyOutputPreservesCauseAndBytes(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires enforced Unix file permissions for the test owner")
	}

	fsys := testfs.NewReal(t)
	in := fsys.WriteFile("trivy.json", []byte(sampleTrivyJSON))
	out := fsys.WriteFile("output.json", []byte("previous output"))
	require.NoError(t, os.Chmod(out, 0o400))
	t.Cleanup(func() { require.NoError(t, os.Chmod(out, 0o600)) })

	count, err := appsecurity.TrivyToGitLabContainer(appsecurity.TransformInput{InputPath: in, OutputPath: out})
	require.Zero(t, count)
	require.ErrorIs(t, err, fs.ErrPermission)

	var cause *os.PathError
	require.ErrorAs(t, err, &cause)
	require.Equal(t, out, cause.Path)
	require.Equal(t, "open", cause.Op)
	require.Equal(t, "previous output", string(fsys.ReadFile("output.json")))
	require.True(t, bytes.Equal([]byte(sampleTrivyJSON), fsys.ReadFile("trivy.json")), "transform changed input bytes")

	info, err := os.Stat(out)
	require.NoError(t, err)
	require.Equal(t, fs.FileMode(0o400), info.Mode().Perm())
}
