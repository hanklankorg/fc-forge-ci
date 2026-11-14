// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
)

func TestAdmitSignedXcodeRunner(t *testing.T) {
	t.Parallel()

	const (
		signed   = `{"version":1,"stage":"build","targets":{"xcode_ios":{"runs":true,"items":[{"name":"app","project_type":"xcode-ios","xcode_ios":{"scheme":"App"}}]}}}`
		unsigned = `{"version":1,"stage":"build","targets":{"xcode_ios":{"runs":true,"items":[{"name":"app","project_type":"xcode-ios","xcode_ios":{"enable_code_signing":false}}]}}}`
		mixed    = `{"targets":{"xcode_ios":{"runs":true,"items":[{"name":"lib","xcode_ios":{"enable_code_signing":false}},{"name":"app","xcode_ios":{"enable_code_signing":true}}]}}}`
		none     = `{"version":1,"stage":"build","targets":{"xcode_ios":{"runs":false,"items":[]},"go":{"runs":true,"items":[{"name":"cli","project_type":"go"}]}}}`
		ownMac   = `["self-hosted", "mac-mini"]`
		hosted   = `["macos-26", "macos-27"]`
	)

	for _, tc := range []struct {
		name, plan, runsOn, allowed string
		refusal                     string
		kind                        error
	}{
		{name: "the caller chooses by default", plan: signed, runsOn: ownMac, allowed: `[]`},
		{name: "an allowed runner signs", plan: signed, runsOn: `["macos-27"]`, allowed: hosted},
		{name: "a runner outside the list does not sign", plan: signed, runsOn: ownMac, allowed: hosted,
			refusal: "runs-on-darwin-arm64 names the runner of a job that holds signing material, and enforce-signer-runs-on-darwin-arm64 does not allow its label self-hosted, mac-mini (allowed: macos-26, macos-27)", kind: errs.ErrUsage},
		{name: "one signed build among unsigned ones is held to the list", plan: mixed, runsOn: ownMac, allowed: hosted, refusal: "self-hosted, mac-mini", kind: errs.ErrUsage},
		{name: "an unsigned build holds no signing material", plan: unsigned, runsOn: ownMac, allowed: hosted},
		{name: "no Xcode build", plan: none, runsOn: ownMac, allowed: hosted},
		{name: "a malformed list is refused without a signed build", plan: none, runsOn: ownMac, allowed: `macos-26`, refusal: "enforce-signer-runs-on-darwin-arm64 must be a JSON array", kind: errs.ErrUsage},
		{name: "a plan without the Xcode target is not read as unsigned", plan: `{"targets":{}}`, runsOn: ownMac, allowed: hosted, refusal: "no coherent xcode_ios target", kind: errs.ErrInvalidConfig},
		{name: "a target that runs nothing is not read as unsigned", plan: `{"targets":{"xcode_ios":{"runs":true,"items":[]}}}`, runsOn: ownMac, allowed: hosted, refusal: "no coherent xcode_ios target", kind: errs.ErrInvalidConfig},
		{name: "an unreadable plan", plan: `{"targets":`, runsOn: ownMac, allowed: hosted, refusal: "parse build-stage plan", kind: errs.ErrInvalidConfig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := pipeline.AdmitSignedXcodeRunner(tc.plan, tc.runsOn, tc.allowed)
			if tc.refusal == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.kind)
			require.ErrorContains(t, err, tc.refusal)
		})
	}
}
