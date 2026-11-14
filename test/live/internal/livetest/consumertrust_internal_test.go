// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/evidence"
)

const (
	trustTestPath    = ".forgejo/release-allowed-signers"
	trustTestSigners = requestSigner + " ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n"
)

// The trust staged before the base commit is in the base and in the change on
// it, which differs from the base by the canary alone and has the base as its
// only parent; the checkout is not written.
func TestConsumer_WithRequestTrustIsInTheBaseAndTheChange(t *testing.T) {
	t.Parallel()
	evidence.RequireTool(t, "ssh-keygen")

	git, root := newConsumerCheckout(t, consumerSHA1)
	before := mustLayerGit(t, git, root, "rev-parse", "HEAD")

	consumer, err := openConsumer(t.Context(), git, root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	trusted, err := consumer.withRequestTrust(t.Context(), trustTestPath, trustTestSigners)
	if err != nil {
		t.Fatal(err)
	}

	workflows, err := trusted.workflows(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	plan, err := sampleLayer().PlanConsumer(workflows, ConsumerFacts{})
	if err != nil {
		t.Fatal(err)
	}

	base, err := trusted.commitRendered(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}

	SSHKey(t, trusted.stage)

	change, err := trusted.commitChange(t.Context(), base, filepath.Join(trusted.stage, sshKeyFile))
	if err != nil {
		t.Fatal(err)
	}

	want := mustLayerGit(t, git, trusted.stage, "rev-parse", trusted.Commit+":"+trustTestPath)
	for _, commit := range []string{base, change} {
		if got := mustLayerGit(t, git, trusted.stage, "rev-parse", commit+":"+trustTestPath); got != want {
			t.Errorf("%s holds %s at %s, want the trust blob %s", commit, got, trustTestPath, want)
		}
	}

	if content := mustLayerGit(t, git, trusted.stage, "cat-file", "-p", base+":"+trustTestPath); content+"\n" != trustTestSigners {
		t.Errorf("the base's trust is %q, want %q", content, trustTestSigners)
	}

	if changed := mustLayerGit(t, git, trusted.stage, "diff-tree", "--name-only", "-r", base, change); changed != consumerCanary {
		t.Errorf("the change differs from the base in %q, want the canary alone", changed)
	}

	if parents := mustLayerGit(t, git, trusted.stage, "rev-list", "--parents", "-n", "1", change); parents != change+" "+base {
		t.Errorf("the change is %q, want one parent, the base", parents)
	}

	if after := mustLayerGit(t, git, root, "rev-parse", "HEAD"); after != before || consumer.Commit != before {
		t.Errorf("the checkout moved from %s to %s, or the source consumer did (%s)", before, after, consumer.Commit)
	}
}

func TestConsumer_WithRequestTrustRefusesPrivateMaterialAndUnsafePaths(t *testing.T) {
	t.Parallel()

	git, root := newConsumerCheckout(t, consumerSHA1)

	consumer, err := openConsumer(t.Context(), git, root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	for name, test := range map[string]struct{ path, signers string }{
		"empty signers":  {trustTestPath, " \n"},
		"private key":    {trustTestPath, "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY-----\n"},
		"absolute path":  {"/" + trustTestPath, trustTestSigners},
		"parent path":    {"../" + trustTestPath, trustTestSigners},
		"root path":      {".", trustTestSigners},
		"empty path":     {"", trustTestSigners},
		"trailing slash": {trustTestPath + "/", trustTestSigners},
	} {
		if got, err := consumer.withRequestTrust(t.Context(), test.path, test.signers); !errors.Is(err, errs.ErrValidation) || got.Commit != consumer.Commit {
			t.Errorf("%s: withRequestTrust = %s, %v; want a validation refusal that leaves the consumer", name, got.Commit, err)
		}
	}
}
