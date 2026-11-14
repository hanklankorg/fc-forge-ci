// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"fmt"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

const gitlabPreseedOriginalBuild = ".gitlab/.livetest-original-build-release.sh"

// WithPreseededGitLabProvenance adds the negative handoff fixture only to the
// private consumer index. The admitted source checkout is never edited, and
// its original build runs before inert provenance is inserted into dist.
func (consumer Consumer) WithPreseededGitLabProvenance(tb TB) Consumer {
	tb.Helper()

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	prepared, err := consumer.withPreseededGitLabProvenance(ctx)
	if err != nil {
		tb.Fatalf("livetest: prepare provenance rejection fixture: %v", err)
	}

	return prepared
}

func (consumer Consumer) withPreseededGitLabProvenance(ctx context.Context) (Consumer, error) {
	listed, err := consumer.git.run(ctx, consumer.stage, "ls-tree", "--name-only", consumer.Commit, "--", gitlabPreseedOriginalBuild)
	if err != nil {
		return Consumer{}, err
	}

	if listed != "" {
		return Consumer{}, fmt.Errorf("consumer already owns the provenance fixture's reserved build path: %w", errs.ErrValidation)
	}

	original, err := githubWorkflow(ctx, consumer, GitLabConsumerBuildScript)
	if err != nil {
		return Consumer{}, err
	}

	if _, err := consumer.git.run(ctx, consumer.stage, "read-tree", consumer.Commit); err != nil {
		return Consumer{}, err
	}

	if err := consumer.stageFile(ctx, "100644", gitlabPreseedOriginalBuild, original); err != nil {
		return Consumer{}, err
	}

	wrapper := "#!/usr/bin/env bash\n# SPDX-FileCopyrightText" + ": 2026 Digg - Agency for Digital Government\n# SPDX-License-Identifier" + ": CC0-1.0\nset -euo pipefail\nbash " + gitlabPreseedOriginalBuild + " \"$@\"\ntest ! -L dist\nmkdir -p dist\nprintf '%s\\n' '{\"fixture\":\"untrusted-provenance\"}' > dist/slsa-provenance.intoto.json\n"
	if err := consumer.stageFile(ctx, "100644", GitLabConsumerBuildScript, wrapper); err != nil {
		return Consumer{}, err
	}

	commit, err := consumer.commit(ctx, consumerBaseMessage)
	if err != nil {
		return Consumer{}, err
	}

	consumer.Commit = commit

	return consumer, nil
}
