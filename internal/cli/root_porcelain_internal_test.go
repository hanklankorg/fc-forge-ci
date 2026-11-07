// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package cli

import (
	"strings"
	"testing"
)

// TestPorcelainCommandsResolve guards the curated direct-use list (the start
// card and the root help's "run directly" block) against drift: every path
// advertised to humans must still resolve to a real command in the tree, and
// the first two must stay init then doctor, which the card's START HERE block
// spells out by hand. A rename that misses this list fails here.
func TestPorcelainCommandsResolve(t *testing.T) {
	t.Parallel()

	root := New(BuildInfo{})

	if porcelainCommands[0].path != "init" || porcelainCommands[1].path != "doctor" {
		t.Fatalf("the card's START HERE block assumes init then doctor lead the list; got %q, %q",
			porcelainCommands[0].path, porcelainCommands[1].path)
	}

	if startHereCount != 3 || porcelainCommands[2].path != "setup status" {
		t.Fatalf("startHereCount = %d, want 3 (init, doctor, setup status)", startHereCount)
	}

	for _, pc := range porcelainCommands {
		cur := root
		for seg := range strings.FieldsSeq(pc.path) {
			next := cur.Command(seg)
			if next == nil {
				t.Fatalf("porcelain command %q: segment %q does not resolve to a command", pc.path, seg)
			}

			cur = next
		}
	}
}
