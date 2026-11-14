// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// An SSH key carries no identity, so a release that signs Git with SSH has no
// default author: the orchestrator's bot-email is the signing principal that
// .forge-ci/allowed_signers must name. The go-cli and Sigstore-keyless
// examples passed RELEASE_SSH_SIGNING_KEY without it, and a release copied
// from them could not make its release commit. Examples of both forges pass a
// placeholder starting with FIXME, which a copied workflow shows as unfinished.
func TestExampleSSHSigningReleasesNameTheirPrincipal(t *testing.T) {
	t.Parallel()

	found := 0

	err := filepath.WalkDir(filepath.Join(reporoot.Path(t), "examples"), func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || filepath.Ext(path) != ".yml" {
			return walkErr
		}

		body, err := os.ReadFile(path)
		require.NoError(t, err)

		var example struct {
			Jobs map[string]struct {
				With    map[string]any `yaml:"with"`
				Secrets any            `yaml:"secrets"`
			} `yaml:"jobs"`
		}
		require.NoError(t, yaml.Unmarshal(body, &example), path)

		for name, job := range example.Jobs {
			secrets, ok := job.Secrets.(map[string]any)
			if !ok {
				continue
			}

			if _, ssh := secrets["RELEASE_SSH_SIGNING_KEY"]; !ssh {
				continue
			}

			found++

			email, _ := job.With["bot-email"].(string)
			if strings.TrimSpace(email) == "" {
				t.Errorf("%s job %s signs Git with SSH but passes no bot-email", path, name)
			} else if !strings.HasPrefix(email, "FIXME") {
				// A placeholder that reads as real is copied unchanged.
				t.Errorf("%s job %s passes bot-email %q, want a FIXME placeholder an adopter must replace", path, name, email)
			}

			// The bundled allowed_signers example must name the same
			// principal, or an adopter who replaces only the key verifies the
			// release against an identity that never signed it.
			signers := filepath.Join(filepath.Dir(filepath.Dir(path)), ".forge-ci", "allowed_signers.example")
			if principals, ok := examplePrincipals(t, signers); ok && !principals[email] {
				t.Errorf("%s job %s passes bot-email %q, which %s does not name", path, name, email, signers)
			}
		}

		return nil
	})
	require.NoError(t, err)
	require.Positive(t, found, "no example signs Git with SSH; the guard checks nothing")
}

// examplePrincipals reads the principals of an OpenSSH allowed_signers file,
// reporting false when the file does not exist.
func examplePrincipals(t *testing.T, path string) (map[string]bool, bool) {
	t.Helper()

	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false
	}

	require.NoError(t, err)

	principals := map[string]bool{}

	for line := range strings.SplitSeq(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}

		for principal := range strings.SplitSeq(fields[0], ",") {
			principals[principal] = true
		}
	}

	return principals, true
}
