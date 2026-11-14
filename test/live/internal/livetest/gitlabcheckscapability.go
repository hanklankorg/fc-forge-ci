// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"errors"
)

//nolint:staticcheck // ST1005: Buildah is a proper noun and the message text is asserted.
//lint:ignore ST1005 Buildah is a proper noun and the message text is asserted.
var errBuildahChecksFixture = errors.New("Buildah checks require the generic Go CLI integration fixture")

// GitLabChecksNeedBuildah recognizes only the generic Go CLI fixture's explicit
// capability marker in the admitted commit. It is used by release acceptance,
// never by the consumer's merge-request gate or ordinary check component.
func (consumer Consumer) GitLabChecksNeedBuildah(tb TB) bool {
	tb.Helper()

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	needed, err := consumer.gitlabChecksNeedBuildah(ctx)
	if err != nil {
		tb.Fatalf("livetest: invalid GitLab checks capability: %v", err)
	}

	return needed
}

func (consumer Consumer) gitlabChecksNeedBuildah(ctx context.Context) (bool, error) {
	const path = ".gitlab/generic-cli-checks"

	listed, err := consumer.git.output(ctx, consumer.stage, nil, "ls-tree", "--name-only", consumer.Commit, "--", path)
	if err != nil || len(listed) == 0 {
		return false, err
	}

	marker, err := githubWorkflow(ctx, consumer, path)
	if err != nil {
		return false, err
	}

	kind, err := githubWorkflow(ctx, consumer, ".gitlab/generic-cli-kind")
	if err != nil {
		return false, err
	}

	if marker != "buildah-integration\n" || kind != "go\n" {
		return false, errBuildahChecksFixture
	}

	return true, nil
}
