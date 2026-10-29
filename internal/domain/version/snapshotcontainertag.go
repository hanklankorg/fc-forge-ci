// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// ComposeSnapshotContainerTag uses a postfix so registry retention can recognize
// snapshots. Every branch token carries its logical branch hash, including
// literal names resembling generated tokens. Reserve room for native arch tags.
func ComposeSnapshotContainerTag(base, branch, shortSHA string) (string, error) {
	if base == "" {
		base = SnapshotVersionDefaultBase
	}
	token := SanitizePathToken(branch)
	if token == "" || len(shortSHA) < SnapshotShortSHALen || strings.Trim(shortSHA, "0123456789abcdef") != "" {
		return "", fmt.Errorf("snapshot container tag requires a usable branch and abbreviated commit")
	}
	budget := 128 - len(base) - len(shortSHA) - len("---snapshot-arm64")
	if budget < 18 {
		return "", fmt.Errorf("snapshot container tag base is too long")
	}
	sum := sha256.Sum256([]byte(branch))
	if len(token) > budget-17 {
		token = token[:budget-17]
	}
	token = strings.TrimRight(token, "-.") + fmt.Sprintf("-%x", sum[:8])
	return fmt.Sprintf("%s-%s-%s-snapshot", base, token, shortSHA), nil
}
