// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"fmt"
	"strings"
	"testing"
)

func TestProtectedPackageInventoryKeepsTheUnionOfConsumerPins(t *testing.T) {
	t.Parallel()

	a, b, c := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64), "sha256:"+strings.Repeat("c", 64)

	groups, err := ParsePackageRetentionPins(fmt.Sprintf(`{"include":[
{"repository":"ghcr.io/acme/runtime","digest":%q},
{"repository":"ghcr.io/acme/other","digest":%q},
{"repository":"ghcr.io/acme/runtime","digest":%q},
{"repository":"ghcr.io/acme/runtime","digest":%q}]}`, a, c, b, a))
	if err != nil {
		t.Fatal(err)
	}

	if len(groups) != 2 || groups[0].Repository != "ghcr.io/acme/runtime" || len(groups[0].Digests) != 2 || groups[1].Digests[0] != c {
		t.Fatalf("pins were not grouped/deduplicated per repository: %+v", groups)
	}

	versions := []PackageVersion{{ID: 1, Digest: a}, {ID: 2, Digest: b}}
	rule := RetentionRule{KeepDigests: groups[0].Digests}

	roots, err := RetentionRoots(versions, rule)
	if err != nil {
		t.Fatal(err)
	}

	if plan := PlanRetention(versions, roots, rule); len(plan.Delete) != 0 {
		t.Fatalf("preview proposed deleting another consumer's protected pin: %+v", plan)
	}
}

func TestProtectedPackageInventoryRefusesTheWholeInvalidDeclaration(t *testing.T) {
	t.Parallel()

	valid := fmt.Sprintf(`{"repository":"ghcr.io/acme/runtime","digest":"sha256:%s"}`, strings.Repeat("a", 64))
	for _, raw := range []string{
		`null`, `{}`, `{"include":[]}`, `{"include":null}`, `{"include":[{}]}`,
		`{"include":[` + valid + `]} {}`, `{"include":[` + valid + `],"other":true}`,
		`{"include":[` + valid + `,{"repository":"docker.io/acme/runtime","digest":"sha256:` + strings.Repeat("b", 64) + `"}]}`,
		`{"include":[` + valid + `,{"repository":"ghcr.io/acme/runtime:latest","digest":"invalid"}]}`,
		strings.Repeat(" ", (1<<20)+1),
	} {
		if groups, err := ParsePackageRetentionPins(raw); err == nil || groups != nil {
			t.Fatalf("invalid inventory produced a partial plan: groups=%v err=%v", groups, err)
		}
	}
}
