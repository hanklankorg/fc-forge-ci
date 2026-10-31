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
// from them could not make its release commit.
func TestExampleSSHSigningReleasesNameTheirPrincipal(t *testing.T) {
	t.Parallel()

	found := 0

	err := filepath.WalkDir(filepath.Join(reporoot.Path(t), "examples"), func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || filepath.Ext(path) != ".yml" || strings.Contains(path, string(filepath.Separator)+"forgejo"+string(filepath.Separator)) {
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

			if email, _ := job.With["bot-email"].(string); strings.TrimSpace(email) == "" {
				t.Errorf("%s job %s signs Git with SSH but passes no bot-email", path, name)
			}
		}

		return nil
	})
	require.NoError(t, err)
	require.Positive(t, found, "no example signs Git with SSH; the guard checks nothing")
}
