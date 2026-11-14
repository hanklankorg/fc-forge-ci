// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"fmt"
	"io/fs"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// WithRequestTrust returns the consumer with a release requester trusted in
// its tree: the public allowed-signers file at path, the one the consumer's
// release workflow names as release-request-allowed-signers. The file goes
// into the private index before Push, so the base commit and every change on
// it carry it, and a release requested from a commit merged from such a change
// finds the trust in place. A standalone release trial commits the same file
// remotely after Push instead; a linked flow cannot, since the change under
// review would then lack it and the merge would carry it away.
//
// Only public material goes in: a line of a private key is refused.
func (consumer Consumer) WithRequestTrust(tb TB, path, allowedSigners string) Consumer {
	tb.Helper()

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	trusted, err := consumer.withRequestTrust(ctx, path, allowedSigners)
	if err != nil {
		tb.Fatalf("livetest: trust the release requester in the consumer: %v", err)
	}

	return trusted
}

func (consumer Consumer) withRequestTrust(ctx context.Context, path, allowedSigners string) (Consumer, error) {
	if strings.TrimSpace(allowedSigners) == "" || strings.Contains(allowedSigners, "PRIVATE KEY") {
		return consumer, fmt.Errorf("the allowed signers must be the requester's public key lines: %w", errs.ErrValidation)
	}

	if path == "." || !fs.ValidPath(path) {
		return consumer, fmt.Errorf("the allowed-signers path %q must be a clean relative path in the consumer: %w", path, errs.ErrValidation)
	}

	if _, err := consumer.git.run(ctx, consumer.stage, "read-tree", consumer.Commit); err != nil {
		return consumer, err
	}

	if err := consumer.stageFile(ctx, "100644", path, allowedSigners); err != nil {
		return consumer, err
	}

	commit, err := consumer.commit(ctx, consumerBaseMessage)
	if err != nil {
		return consumer, err
	}

	consumer.Commit = commit

	return consumer, nil
}
