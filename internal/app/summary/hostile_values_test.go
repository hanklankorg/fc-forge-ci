// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package summary_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	appsummary "github.com/diggsweden/forge-ci/internal/app/summary"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

const (
	hostileLink   = "[docs](https://attacker.invalid/phish)"
	hostileHTML   = "<img src=x onerror=alert(1)>"
	syntheticCred = "synthetic-credential-4b7e"
)

// assertLiteralSummary fails when rendered Markdown carries a hostile value in
// a form the renderer would interpret: link syntax, an HTML tag, emphasis, or a
// synthetic credential.
func assertLiteralSummary(t *testing.T, body string) {
	t.Helper()

	for _, raw := range []string{"](https://attacker.invalid", "<img", "**npm**", syntheticCred} {
		if strings.Contains(body, raw) {
			t.Errorf("summary carries %q:\n%s", raw, body)
		}
	}
}

// TestSummaries_HostileValuesStayLiteral feeds link syntax, HTML and emphasis
// into the publish renderers, and TestSummaries_RunSummariesKeepCellsAndDrop-
// CredentialedLinks carries them with credential-bearing URLs into the release,
// pull-request and snapshot renderers, one value per call site. The publish summaries used to write their
// version, registry, package type and repository as Markdown; a resource URL
// with userinfo used to become a link that published the credential; the
// snapshot log banner printed a line break in the branch, which starts a new
// log line the runner may read as a workflow command. Every value now renders
// literally, the credentialed link is "not available", and the banner keeps
// each value on its own line.
func TestSummaries_HostileValuesStayLiteral(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 10, 14, 0, 0, 0, time.UTC)

	t.Run("maven central publish", func(t *testing.T) {
		t.Parallel()

		sink := &fakeSummarySink{}
		if err := appsummary.MavenCentralPublish(context.Background(), sink, appsummary.MavenCentralPublishInput{Version: "1.2.3" + hostileHTML, Now: now}); err != nil {
			t.Fatal(err)
		}

		assertLiteralSummary(t, sink.buf.String())

		if !strings.Contains(sink.buf.String(), "- **Version:** 1.2.3&#60;img src=x onerror=alert(1)&#62;\n") {
			t.Errorf("version not rendered literally:\n%s", sink.buf.String())
		}
	})

	t.Run("forge packages publish", func(t *testing.T) {
		t.Parallel()

		sink := &fakeSummarySink{}
		if err := appsummary.ForgePackagesPublish(context.Background(), sink, appsummary.ForgePackagesPublishInput{
			Repository: "org/" + hostileHTML, PackageType: "**npm**", RegistryName: hostileLink, Now: now,
		}); err != nil {
			t.Fatal(err)
		}

		assertLiteralSummary(t, sink.buf.String())

		for _, want := range []string{
			"## Published to &#91;docs&#93;(https&#58;//attacker.invalid/phish) 📦\n",
			"- **Package Type:** &#42;&#42;npm&#42;&#42;\n",
			"- **Repository:** org/&#60;img src=x onerror=alert(1)&#62;\n",
		} {
			if !strings.Contains(sink.buf.String(), want) {
				t.Errorf("missing %q in:\n%s", want, sink.buf.String())
			}
		}
	})
}

// TestSummaries_RunSummariesKeepCellsAndDropCredentialedLinks carries the same
// adversaries into the release, pull-request and snapshot renderers.
func TestSummaries_RunSummariesKeepCellsAndDropCredentialedLinks(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 10, 14, 0, 0, 0, time.UTC)
	credentialURL := "https://ci-user:" + syntheticCred + "@forge.example/org/app/actions/runs/1"

	t.Run("release summary", func(t *testing.T) {
		t.Parallel()

		sink := &fakeSummarySink{}
		if err := appsummary.ReleaseSummary(context.Background(), sink, appsummary.ReleaseSummaryInput{
			ReleaseVersion: "v1`" + hostileLink + "`", ReleaseBranch: "main|x", ReleaseCommit: strings.Repeat("a", 40), ReleaseActor: "bot|x",
			RunURL: credentialURL, Now: now,
		}); err != nil {
			t.Fatal(err)
		}

		body := sink.buf.String()
		assertLiteralSummary(t, strings.ReplaceAll(body, "'[docs](https://attacker.invalid/phish)'", ""))

		if !strings.Contains(body, "- Workflow Run: not available\n") || !strings.Contains(body, "| **Branch** | `main\\|x` |") {
			t.Errorf("credentialed run URL linked or branch cell broken:\n%s", body)
		}

		if !strings.Contains(body, "| **Version** | `v1'[docs](https://attacker.invalid/phish)'` |") {
			t.Errorf("version did not stay inside its code span:\n%s", body)
		}

		// The actor is plain text, not a code span, so a backslash escape
		// would render literally; the entity is what keeps the cell whole.
		if !strings.Contains(body, "| **Released By** | @bot&#124;x |") {
			t.Errorf("actor cell was not kept whole as literal text:\n%s", body)
		}
	})

	t.Run("pull request summary", func(t *testing.T) {
		t.Parallel()

		sink := &fakeSummarySink{}
		if err := appsummary.PRSummary(context.Background(), sink, appsummary.PRSummaryInput{
			ProjectType: "go", Branch: "feature\n| **Injected** | row |", Commit: strings.Repeat("b", 40), Actor: "dev", RunURL: credentialURL, Now: now,
		}); err != nil {
			t.Fatal(err)
		}

		body := sink.buf.String()
		assertLiteralSummary(t, body)

		if strings.Contains(body, "\n| **Injected** |") || !strings.Contains(body, "- Workflow Run: not available\n") {
			t.Errorf("branch injected a row or credentialed URL linked:\n%s", body)
		}
	})

	t.Run("snapshot banner", func(t *testing.T) {
		t.Parallel()

		sink := &fakeSummarySink{}

		var log bytes.Buffer

		if err := appsummary.SnapshotReleaseSummary(context.Background(), sink, &log, appsummary.SnapshotReleaseSummaryInput{
			ProjectType: projecttype.Go, ReleaseRef: "main\n::add-mask::" + syntheticCred, ReleaseSHA: strings.Repeat("c", 40), ReleaseActor: "dev",
			RunURL: credentialURL, Now: now,
		}); err != nil {
			t.Fatal(err)
		}

		for line := range strings.SplitSeq(log.String(), "\n") {
			if strings.HasPrefix(line, "::") {
				t.Errorf("banner started a workflow-command line: %q\n%s", line, log.String())
			}
		}

		if strings.Contains(sink.buf.String(), "](https://ci-user") || !strings.Contains(sink.buf.String(), "- Workflow Run: not available\n") {
			t.Errorf("credentialed run URL linked:\n%s", sink.buf.String())
		}
	})
}

// TestBuildSummaries_HostileValuesCannotForgeRows is the escaping row for the
// build renderers the `build <ecosystem> run` verbs call.
//
// Each renders caller-supplied values into a Markdown step summary, and that
// summary is what a reviewer reads to decide a release looks right. A value
// carrying a newline used to inject whole bullet rows: a version of
// "1.0\n- **Signed:** yes" produced a summary claiming the build was signed.
//
// The canary is a value that would become a new row and a markup break if it
// reached the summary unescaped. What is asserted is the absence of the
// forged row, not a particular encoding, so the helpers stay free to change
// how they escape.
func TestBuildSummaries_HostileValuesCannotForgeRows(t *testing.T) {
	t.Parallel()

	const hostile = "1.0\n- **Signed:** ✓ yes\n- **Reviewed:** yes"

	for name, render := range map[string]func(*fakeSummarySink) error{
		"maven": func(sink *fakeSummarySink) error {
			return appsummary.MavenBuild(context.Background(), sink, appsummary.MavenBuildInput{
				BuildType: "library", GroupID: "com.example", ArtifactID: "app", Version: hostile, JavaVersion: "21",
			})
		},
		"npm": func(sink *fakeSummarySink) error {
			return appsummary.NPMBuild(context.Background(), sink, appsummary.NPMBuildInput{
				PackageName: "@org/app", Version: hostile, NodeVersion: "22",
			})
		},
		"gradle": func(sink *fakeSummarySink) error {
			return appsummary.GradleBuild(context.Background(), sink, appsummary.GradleBuildInput{
				JavaVersion: "21", GradleTasks: "build", Version: hostile,
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sink := &fakeSummarySink{}
			if err := render(sink); err != nil {
				t.Fatal(err)
			}

			body := sink.buf.String()

			// The forged row must not appear AS A ROW. The characters may well
			// survive inside the value -- escaping renders them inert rather
			// than deleting them -- so what is checked is a line that opens
			// with the summary's own bullet syntax and the injected label.
			for line := range strings.SplitSeq(body, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "- **Signed:") {
					t.Errorf("a value forged a summary row:\n%s", body)

					break
				}
			}

			// And the summary is still one block: the injected newlines must
			// not have split it.
			if !strings.Contains(body, "Build Summary") {
				t.Errorf("the summary lost its heading:\n%s", body)
			}
		})
	}
}
