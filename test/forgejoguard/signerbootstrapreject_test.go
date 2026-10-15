// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// signerJobYAML builds one signer job. Fields are substituted so each rejection
// case differs from the accepted shape in exactly one way.
func signerJobYAML(strict, step1, step2, step3, step4, extra string) string {
	return `
jobs:
  sign:
    container:
      image: ` + signerImage + `
    env:
      SIGNER_STRICT: '` + strict + `'
    steps:
      - name: ` + step1 + `
        run: git clone helpers
      - name: ` + step2 + `
        run: git rev-parse HEAD
      - name: ` + step3 + `
        run: fetch_forge_ci
      - name: ` + step4 + `
        run: bash .forgejo/scripts/verify-signer-toolchain.sh
` + extra
}

func acceptedSignerJob(extra string) string {
	return signerJobYAML("1", bootstrapSteps()[0], bootstrapSteps()[1], bootstrapSteps()[2], bootstrapSteps()[3], extra)
}

// The checker accepts the correct shape and rejects each way of breaking it.
// Without these, a checker that never fails would still look healthy.
func TestSignerBootstrapRejections(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"accepted", acceptedSignerJob(""), ""},
		{
			"step order drifts",
			signerJobYAML("1", bootstrapSteps()[0], "Verify drifted helper checkout", bootstrapSteps()[2], bootstrapSteps()[3], ""),
			"want \"Verify trusted helper checkout\"",
		},
		{
			"the binary is fetched after the toolchain check",
			signerJobYAML("1", bootstrapSteps()[0], bootstrapSteps()[1], bootstrapSteps()[3], bootstrapSteps()[2], ""),
			"want \"Fetch engine binary\"",
		},
		{
			"the binary is left to the first engine call",
			strings.Replace(acceptedSignerJob(""), "        run: fetch_forge_ci", "        run: true", 1),
			"must download the engine binary with fetch_forge_ci",
		},
		{
			"the fetch step holds a secret",
			strings.Replace(acceptedSignerJob(""),
				"        run: fetch_forge_ci",
				"        env:\n          PACKAGE_TOKEN: ${{ secrets.TOKEN }}\n        run: fetch_forge_ci", 1),
			"references a secret",
		},
		{
			"strict mode disabled",
			signerJobYAML("0", bootstrapSteps()[0], bootstrapSteps()[1], bootstrapSteps()[2], bootstrapSteps()[3], ""),
			"SIGNER_STRICT",
		},
		{
			"secret referenced before the verifier",
			strings.Replace(acceptedSignerJob(""),
				"        run: git clone helpers",
				"        env:\n          ACCIDENTAL: ${{ secrets['TOKEN'] }}\n        run: git clone helpers", 1),
			"references a secret",
		},
		{
			"dotted secret referenced before the verifier",
			strings.Replace(acceptedSignerJob(""),
				"        run: git clone helpers",
				"        env:\n          ACCIDENTAL: ${{ secrets.TOKEN }}\n        run: git clone helpers", 1),
			"references a secret",
		},
		{
			"engine runs before the verifier",
			strings.Replace(acceptedSignerJob(""),
				"        run: git rev-parse HEAD",
				"        run: run_forge_ci release sign", 1),
			"runs the engine",
		},
		{
			"a later step overrides strict mode",
			acceptedSignerJob("      - name: Sign\n        env:\n          SIGNER_STRICT: '0'\n        run: true\n"),
			"overrides SIGNER_STRICT",
		},
		{
			"step four does not run the toolchain verifier",
			strings.Replace(acceptedSignerJob(""),
				"        run: bash .forgejo/scripts/verify-signer-toolchain.sh",
				"        run: true", 1),
			"must run verify-signer-toolchain.sh",
		},
		{
			"job has too few steps",
			`
jobs:
  sign:
    container:
      image: ` + signerImage + `
    env:
      SIGNER_STRICT: '1'
    steps:
      - name: ` + bootstrapSteps()[0] + `
        run: true
`,
			"want at least 4",
		},
	} {
		err := checkSignerBootstrap([]byte(test.yaml), 1)
		switch {
		case test.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected error: %v", test.name, err)
		case test.wantErr != "" && err == nil:
			t.Errorf("%s: accepted a workflow it must reject", test.name)
		case test.wantErr != "" && !strings.Contains(err.Error(), test.wantErr):
			t.Errorf("%s: error %q does not mention %q", test.name, err, test.wantErr)
		}
	}
}

// A wrong signer-job count is rejected, so a new signer job cannot be added
// without review.
func TestSignerJobCountIsPinned(t *testing.T) {
	t.Parallel()

	if err := checkSignerBootstrap([]byte(acceptedSignerJob("")), 2); err == nil {
		t.Error("accepted one signer job where two were expected")
	}
}

// The same drift the shell contract injected into the real workflow is still
// rejected when the checker reads the real file.
func TestRealSignerWorkflowRejectsFinalJobDrift(t *testing.T) {
	t.Parallel()

	const relative = ".forgejo/workflows/sign-and-publish-release.yml"

	data := string(reporoot.ReadFile(t, relative))

	marker := "- name: " + bootstrapSteps()[1]

	index := strings.LastIndex(data, marker)
	if index < 0 {
		t.Fatalf("%s does not contain %q", relative, marker)
	}

	drifted := data[:index] + "- name: Verify drifted helper checkout" + data[index+len(marker):]

	if err := checkSignerBootstrap([]byte(drifted), signerWorkflows()[relative]); err == nil {
		t.Error("accepted final-job step-order drift in the real signer workflow")
	}
}
