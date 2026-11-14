// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrPublicTransparency refuses a trial that would write to a public
// transparency log: an entry there is permanent.
var ErrPublicTransparency = errors.New("a live trial writes nothing to a public transparency log")

// A shipped GitHub release is keyless, as its example is: it signs with the
// run's identity and writes every signature to the public Sigstore log. A
// hosted trial signs with a Cosign key made for it and no log instead, the
// way the lab suite signs everywhere (FORGE_CI_COSIGN_TRANSPARENCY=none), so
// the release still signs, attests and verifies and nothing is published.
const trialKeySignBlock = "sign:\n  method: kms\n  key: env://COSIGN_KEY\n  transparency: none\n"

var (
	artifactsSignBlock    = regexp.MustCompile(`(?m)^sign:[ \t]*\n(?:[ \t]+.*\n|[ \t]*\n)*`)
	artifactsSignSetting  = regexp.MustCompile(`(?m)^[ \t]+(method|transparency):[ \t]*([a-z]+)[ \t]*$`)
	artifactsFilesMethod  = regexp.MustCompile(`(?m)^[ \t]+files-method:[ \t]*([a-z]+)[ \t]*(?:#.*)?\n`)
	releaseTokenSecret    = regexp.MustCompile(`(?m)^([ \t]+)RELEASE_TOKEN:[ \t]*\$\{\{[ \t]*secrets\.RELEASE_TOKEN[ \t]*\}\}[ \t]*\n`)
	releaseArtifactsInput = regexp.MustCompile(`(?m)^([ \t]+)artifacts-config:.*\n`)
)

// ReleaseWritesPublicLog reads an artifacts config's sign block: whether a
// release signed with it writes to the public transparency log, which the
// product does by default unless it signs with GPG or sets transparency none.
func ReleaseWritesPublicLog(artifacts string) bool {
	settings := map[string]string{}

	for _, setting := range artifactsSignSetting.FindAllStringSubmatch(artifactsSignBlock.FindString(artifacts), -1) {
		settings[setting[1]] = setting[2]
	}

	return settings["method"] != "gpg" && settings["transparency"] != cosignTransparencyNone
}

// TrialKeyArtifacts is an artifacts config with its sign block replaced by the
// trial key's, or given one when it has none. The trial key replaces the
// Cosign method alone: a declared files-method stays, so the release still
// signs its files the way it declares.
func TrialKeyArtifacts(artifacts string) (string, error) {
	switch len(artifactsSignBlock.FindAllStringIndex(artifacts, -1)) {
	case 0:
		return strings.TrimRight(artifacts, "\n") + "\n" + trialKeySignBlock, nil
	case 1:
		block := trialKeySignBlock
		if files := artifactsFilesMethod.FindStringSubmatch(artifactsSignBlock.FindString(artifacts)); files != nil {
			block += "  files-method: " + files[1] + "\n"
		}

		return artifactsSignBlock.ReplaceAllLiteralString(artifacts, block), nil
	default:
		return "", fmt.Errorf("%w: the artifacts config has more than one sign block", ErrPublicTransparency)
	}
}

// TrialKeyRelease is a keyless release workflow handed the trial's Cosign
// key: the two secrets beside RELEASE_TOKEN, and the public key and its
// digest beside artifacts-config. The digest is a placeholder the caller
// overrides with the key's own. A workflow without exactly one of each
// anchor is refused, not signed keyless.
func TrialKeyRelease(workflow string) (string, error) {
	if len(releaseTokenSecret.FindAllStringIndex(workflow, -1)) != 1 || len(releaseArtifactsInput.FindAllStringIndex(workflow, -1)) != 1 {
		return "", fmt.Errorf("%w: the release workflow must pass RELEASE_TOKEN and artifacts-config once each to take the trial's key", ErrPublicTransparency)
	}

	workflow = releaseTokenSecret.ReplaceAllString(workflow, "${0}${1}RELEASE_COSIGN_PRIVATE_KEY: $${{ secrets.RELEASE_COSIGN_PRIVATE_KEY }}\n"+
		"${1}RELEASE_COSIGN_PASSPHRASE: $${{ secrets.RELEASE_COSIGN_PASSPHRASE }}\n")

	return releaseArtifactsInput.ReplaceAllString(workflow, "${0}${1}cosign-public-key: $${{ vars.COSIGN_PUBLIC_KEY }}\n"+
		"${1}cosign-public-key-sha256: "+unrunDigest+"\n"), nil
}

// WithTrialKeySigning makes PushConsumer hand the named fixture workflow the
// trial's Cosign key (TrialKeyRelease) and commit the artifacts config it
// names with the trial key's sign block (TrialKeyArtifacts).
func (trial *GitHubTrial) WithTrialKeySigning(workflow, config, artifacts string) *GitHubTrial {
	trial.tb.Helper()

	rekeyed, err := TrialKeyArtifacts(artifacts)
	if err != nil {
		trial.tb.Fatalf("livetest: %s: %v", config, err)
	}

	shown := trial.WithFile(config, rekeyed)
	shown.keyWorkflow = workflow

	return shown
}
