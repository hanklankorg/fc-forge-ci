// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package plan_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/diggsweden/forge-ci/internal/cli"
)

func TestValidateTestSelectionsCLIValidatesWholeMapsBeforeOutput(t *testing.T) {
	plan := `{"version":1,"stage":"build","targets":{"cargo":{"runs":true,"items":[{"name":"rust","project_type":"cargo","cargo":{}}]}}}`
	for index, mapping := range []string{`{"rust":{"runs":[{"kind":"package-test","package":"app","name":"guard","gate":"policy"}]}}`, `{"typo":{"runs":[]}}`, `[]`, `{"rust":null}`, `{}`, `{"rust":{"runs":[{"kind":"package-test","package":"app","name":"guard","gate":"policy"}],"runſ":[{"kind":"package-bin","package":"app","name":"main","gate":"application"}]}}`} {
		var out, stderr bytes.Buffer
		cmd := cli.New(cli.BuildInfo{})
		cmd.Writer, cmd.ErrWriter = &out, &stderr
		err := cmd.Run(t.Context(), []string{"forge-ci", "--provider", "local", "--runner", "local", "--json", "plan", "validate-test-selections", "--build-stage-plan-json", plan, "--cargo-test-selections-json", mapping})
		var outputs map[string]string
		_ = json.Unmarshal(out.Bytes(), &outputs)
		if index == 0 {
			if err != nil || outputs["cargo-test-selections-json"] == "" || outputs["go-test-selections-json"] != "{}" {
				t.Fatalf("valid CLI contract failed: %v %s", err, &out)
			}
		} else if err == nil || outputs["cargo-test-selections-json"] != "" || outputs["go-test-selections-json"] != "" {
			t.Fatalf("invalid CLI map emitted outputs: %v %s", err, &out)
		}
	}
}

func TestValidateTestSelectionsCLIRejectsNullOptionalGoRun(t *testing.T) {
	plan := `{"version":1,"stage":"build","targets":{"go":{"runs":true,"items":[{"name":"go-cli","project_type":"go","go":{}}]}}}`
	var out, stderr bytes.Buffer
	cmd := cli.New(cli.BuildInfo{})
	cmd.Writer, cmd.ErrWriter = &out, &stderr
	err := cmd.Run(t.Context(), []string{"forge-ci", "--provider", "local", "--runner", "local", "--json", "plan", "validate-test-selections", "--build-stage-plan-json", plan, "--go-test-selections-json", `{"go-cli":{"packages":["./..."],"gate":"policy","run":null}}`})
	if err == nil || out.Len() != 0 {
		t.Fatalf("invalid optional field emitted output: %v %s", err, &out)
	}
}

func TestValidateTestSelectionsCLIOmissionKeepsLegacyCompatibility(t *testing.T) {
	plan := `{"version":1,"stage":"build","targets":{"cargo":{"runs":true,"items":[{"name":"rust","project_type":"cargo","cargo":{}}]}}}`
	var out, stderr bytes.Buffer
	cmd := cli.New(cli.BuildInfo{})
	cmd.Writer, cmd.ErrWriter = &out, &stderr
	err := cmd.Run(t.Context(), []string{"forge-ci", "--provider", "local", "--runner", "local", "--json", "plan", "validate-test-selections", "--build-stage-plan-json", plan})
	var outputs map[string]string
	_ = json.Unmarshal(out.Bytes(), &outputs)
	if err != nil || outputs["go-test-selections-json"] != "{}" || outputs["cargo-test-selections-json"] != "{}" {
		t.Fatalf("omission compatibility changed: %v %s", err, &out)
	}
}
