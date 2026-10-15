// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package conformance_test

import (
	"strings"
	"testing"
)

// gitlabNativeArtifactProbe uses a clean workspace per job and a payload bound
// to this pipeline and commit, including binary bytes and a trailing newline.
func gitlabNativeArtifactProbe() string {
	return `stages: [produce, verify]
variables:
  GIT_STRATEGY: empty
detect:
  stage: produce
  script:
    - |
      mkdir native-artifact
      printf 'gitlab-native-artifact:%s:%s\000\377\n' "$CI_PIPELINE_ID" "$CI_COMMIT_SHA" > native-artifact/payload.bin
  artifacts:
    paths: [native-artifact/payload.bin]
    expire_in: 1 hour
consume:
  stage: verify
  needs:
    - job: detect
      artifacts: true
  script:
    - |
      test -f native-artifact/payload.bin
      printf 'gitlab-native-artifact:%s:%s\000\377\n' "$CI_PIPELINE_ID" "$CI_COMMIT_SHA" > expected.bin
      cmp expected.bin native-artifact/payload.bin
`
}

func TestProbeFixtures_NativeArtifactConsumerRejectsMissingStaleOrCorruptBytes(t *testing.T) {
	t.Parallel()

	fixture := decodeProbeFixture(t, gitlabNativeArtifactProbe())
	if len(fixture.Detect.Artifacts.Paths) != 1 || fixture.Detect.Artifacts.Paths[0] != "native-artifact/payload.bin" ||
		len(fixture.Consume.Needs) != 1 || fixture.Consume.Needs[0].Job != "detect" || !fixture.Consume.Needs[0].Artifacts {
		t.Fatal("native artifact consumer must download the producer's payload")
	}

	for _, tc := range []struct {
		name, mutation string
		valid          bool
	}{
		{name: "intact", valid: true},
		{name: "missing", mutation: "rm native-artifact/payload.bin\n"},
		{name: "corrupt", mutation: "printf x >> native-artifact/payload.bin\n"},
		{name: "stale", mutation: "CI_PIPELINE_ID=previous-pipeline\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := strings.Join(fixture.Detect.Script, "\n") + "\n" + tc.mutation + strings.Join(fixture.Consume.Script, "\n")

			err := runProbeShell(t, script, "CI_PIPELINE_ID=812", "CI_COMMIT_SHA=0123456789012345678901234567890123456789")
			if (err == nil) != tc.valid {
				t.Fatalf("artifact consumer exit=%v, valid=%t", err, tc.valid)
			}
		})
	}
}
