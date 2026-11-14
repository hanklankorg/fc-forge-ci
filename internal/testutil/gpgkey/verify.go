// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gpgkey

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// VerifyDetachedAt checks that subject.asc is a signature of subject by the
// key, made at the Unix time created: gpg must verify it and report a
// VALIDSIG line for the key's fingerprint with that signature time.
func (k *Key) VerifyDetachedAt(t *testing.T, subject, created string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	//nolint:gosec // test infra; GNUPGHOME is the owned private keyring and subject the test's file.
	out, err := exec.CommandContext(ctx, "gpg", "--homedir", k.GNUPGHOME, "--batch", "--status-fd", "1",
		"--verify", subject+".asc", subject).CombinedOutput()
	if err != nil {
		t.Errorf("%s.asc does not verify with the signing key: %v\n%s", subject, err, out)

		return
	}

	valid := false

	for line := range strings.SplitSeq(string(out), "\n") {
		// [GNUPG:] VALIDSIG <fingerprint> <date> <signature timestamp> ...
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "[GNUPG:]" || fields[1] != "VALIDSIG" {
			continue
		}

		valid = true

		if fields[2] != k.Fingerprint {
			t.Errorf("%s.asc was made by %s, want %s", subject, fields[2], k.Fingerprint)
		}

		if fields[4] != created {
			t.Errorf("%s.asc was made at %s, want %s", subject, fields[4], created)
		}
	}

	if !valid {
		t.Errorf("gpg reported no valid signature in %s.asc:\n%s", subject, out)
	}
}

// CommitAt writes a commit committed at the Unix time committed into a new
// repository in dir and returns its SHA: the release commit a signature is
// dated by.
func CommitAt(t *testing.T, dir, committed string) string {
	t.Helper()

	git := func(args ...string) string {
		//nolint:gosec // test infra; dir and args are the test's.
		command := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir,
			"-c", "user.name=Fixture Bot", "-c", "user.email=bot@invalid.example"}, args...)...)
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "LANG=C", "LC_ALL=C",
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_COMMITTER_DATE=@" + committed + " +0000"}

		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %q: %v\n%s", args, err, out)
		}

		return strings.TrimSpace(string(out))
	}

	git("init", "-q")

	return git("commit-tree", git("mktree"), "-m", "release")
}
