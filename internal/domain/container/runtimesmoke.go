// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"fmt"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	gitdomain "github.com/diggsweden/forge-ci/internal/domain/git"
)

// RuntimeSmokeReceipt binds successful native execution to exact candidate bytes.
// A successful job from an earlier attempt does not qualify a rebuilt digest.
type RuntimeSmokeReceipt struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Arch   string `json:"arch"`
	Ref    string `json:"ref"`
}

// VerifyRuntimeSmokeReceipt refuses incomplete expectations and stale receipts.
func VerifyRuntimeSmokeReceipt(receipt, expected RuntimeSmokeReceipt) error {
	if !gitdomain.ValidCommitSHA(expected.Source) || !runtimeCoordinate.MatchString(expected.Target) ||
		(expected.Arch != ArchAMD64 && expected.Arch != ArchARM64) || !ValidDigestPinnedRef(expected.Ref) {
		return fmt.Errorf("runtime smoke verification requires source, target, native architecture and digest-pinned candidate: %w", errs.ErrUsage)
	}

	if receipt != expected {
		return fmt.Errorf("candidate %s/%s does not match its successful smoke receipt; rerun collect and smoke for rebuilt candidates: %w", expected.Target, expected.Arch, errs.ErrValidation)
	}

	return nil
}
