// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package validate_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	appvalidate "github.com/diggsweden/forge-ci/internal/app/validate"
	"github.com/diggsweden/forge-ci/internal/domain/config"
	domainenforcement "github.com/diggsweden/forge-ci/internal/domain/enforcement"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
)

func runJVMReproWithPolicy(t *testing.T, plan string, strict bool) (string, string, error) {
	t.Helper()

	var out, annotations bytes.Buffer

	err := appvalidate.JVMReproducibilityWithPolicy(t.Context(), &out, output.NewAnnotator(&annotations, output.FormatGitHub),
		appvalidate.JVMReproducibilityInput{ConfigPlanJSON: plan}, domainenforcement.Policy{EnforceReproducibleBuilds: strict})

	return out.String(), annotations.String(), err
}

const pomWithoutTimestamp = `<?xml version="1.0"?>
<project><modelVersion>4.0.0</modelVersion><groupId>g</groupId><artifactId>a</artifactId><version>1.0.0</version></project>`

// A missing setting is a reproducible-builds finding: strict policy blocks it,
// report-only policy reports the same finding and its fix without blocking.
func TestJVMReproducibility_MissingSettingFollowsAdminPolicy(t *testing.T) {
	for _, strict := range []bool{true, false} {
		t.Run(map[bool]string{true: "strict", false: "report-only"}[strict], func(t *testing.T) {
			fsys := testfs.NewReal(t)
			fsys.WriteFile("pom.xml", []byte(pomWithoutTimestamp))
			fsys.WriteFile("app/build.gradle.kts", []byte("plugins { java }\n"))
			fsys.Chdir()

			plan := validationPlanJSON(t, validationConfigPlan(t,
				config.Artifact{Name: "svc", ProjectType: projecttype.Maven, WorkingDirectory: "."}, config.Artifact{Name: "app", ProjectType: projecttype.Gradle, WorkingDirectory: "app"}))
			out, annotations, err := runJVMReproWithPolicy(t, plan, strict)

			if strict != (err != nil) || (strict && !errors.Is(err, errs.ErrValidation)) {
				t.Fatalf("strict=%t err=%v", strict, err)
			}

			for _, want := range []string{"reproducible-builds result=findings count=2", "enforcement=" + map[bool]string{true: "true", false: "false"}[strict], "Fix: add to pom.xml", "Fix: add to build.gradle"} {
				if !strings.Contains(out, want) {
					t.Errorf("stdout lacks %q:\n%s", want, out)
				}
			}

			severity := map[bool]string{true: "::error", false: "::warning"}[strict]
			if strings.Count(annotations, severity) != 2 {
				t.Errorf("want two %s annotations:\n%s", severity, annotations)
			}
		})
	}
}

// Report-only never waives a build file that could not be judged.
func TestJVMReproducibility_ReportOnlyStillBlocksUnreadableBuildFiles(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"malformed pom":          {"pom.xml": "<project><properties>"},
		"no Gradle build script": {"app/settings.gradle.kts": "\n"},
	} {
		t.Run(name, func(t *testing.T) {
			fsys := testfs.NewReal(t)
			for path, body := range files {
				fsys.WriteFile(path, []byte(body))
			}
			fsys.Chdir()

			kind, dir := projecttype.Maven, "."
			if name == "no Gradle build script" {
				kind, dir = projecttype.Gradle, "app"
			}

			out, _, err := runJVMReproWithPolicy(t, configPlanJSON(t, kind, "x", dir), false)
			if !errors.Is(err, errs.ErrValidation) || strings.Contains(out, "result=") {
				t.Fatalf("err=%v out=%s", err, out)
			}
		})
	}
}
