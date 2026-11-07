// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package releasesetup

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/version"
)

// Git is what the release-request check reads from the local repository.
type Git interface {
	// Output runs git and returns stdout; a non-zero exit is an error.
	Output(ctx context.Context, args ...string) (string, error)
	TagExists(ctx context.Context, tag string) (bool, error)
}

// RequestInput checks one release request before it is pushed.
type RequestInput struct {
	Root    string
	Version string // v1.2.3 or 1.2.3; a prerelease needs release-policy stable-prerelease
	Remote  string // empty is origin
	Git     Git
}

// RequestCheck is one precondition of a release request.
type RequestCheck struct {
	OK     bool   `json:"ok"`
	What   string `json:"what"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// RequestPlan is the checks and, once they pass, the commands that make the
// request. The binary prints the commands and does not run them: creating
// and pushing a signed tag is the person's act, and the signature is theirs.
type RequestPlan struct {
	Version  string         `json:"version"`
	Tag      string         `json:"tag"`
	Checks   []RequestCheck `json:"checks"`
	Commands []string       `json:"commands"`
}

// Ready reports whether every check passed.
func (p RequestPlan) Ready() bool {
	for _, c := range p.Checks {
		if !c.OK {
			return false
		}
	}

	return true
}

// PlanRequest checks the version, the local signing setup and the tags, and
// returns the git commands that request the release.
func PlanRequest(ctx context.Context, in RequestInput) (RequestPlan, error) {
	// The release admits the tag under its caller's release-policy, which the
	// binary cannot see here; the most permissive policy checks the form.
	tag := "v" + strings.TrimPrefix(in.Version, "v")
	if _, err := version.ValidateReleaseTag(tag, version.ReleasePolicyStablePrerelease); err != nil {
		// The version is the person's argument: a usage error (exit 2).
		return RequestPlan{}, fmt.Errorf("release-request: %w: %w", err, errs.ErrUsage)
	}

	remote := in.Remote
	if remote == "" {
		remote = "origin"
	}

	plan := RequestPlan{Version: tag, Tag: version.ReleaseRequestPrefix + tag}
	plan.Checks = append(plan.Checks, signingChecks(ctx, in)...)
	plan.Checks = append(plan.Checks, tagChecks(ctx, in.Git, plan.Tag, tag)...)
	plan.Commands = []string{
		fmt.Sprintf("git tag -s %s -m %q", plan.Tag, "Release "+tag),
		fmt.Sprintf("git push %s %s", remote, plan.Tag),
	}

	return plan, nil
}

func signingChecks(ctx context.Context, in RequestInput) []RequestCheck {
	get := func(key string) string {
		out, err := in.Git.Output(ctx, "config", "--get", key)
		if err != nil {
			return ""
		}

		return strings.TrimSpace(out)
	}

	checks := make([]RequestCheck, 0, 3)

	format := get("gpg.format")
	checks = append(checks, RequestCheck{
		OK: format == "ssh", What: "git signs tags with SSH",
		Detail: "gpg.format=" + valueOr(format, "(unset: openpgp)"),
		Fix:    "git config gpg.format ssh",
	})

	key := get("user.signingkey")
	checks = append(checks, RequestCheck{
		OK: key != "", What: "git has a signing key",
		Detail: "user.signingkey=" + valueOr(key, "(unset)"),
		Fix:    "git config user.signingkey ~/.ssh/id_ed25519.pub",
	})

	if key == "" || format != "ssh" {
		return checks
	}

	file := DefaultSignersFile(in.Root)
	public, err := signingPublicKey(key)

	allowed := false

	if err == nil {
		body, _ := os.ReadFile(filepath.Join(in.Root, filepath.FromSlash(file))) //nolint:gosec // repository allowlist under root.
		allowed = hasEntry(body, "", public)
	}

	detail := file + " lists your signing key"
	if err != nil {
		detail = err.Error()
	} else if !allowed {
		detail = file + " does not list your signing key; the release refuses the request"
	}

	return append(checks, RequestCheck{
		OK: allowed, What: "the release trusts your key",
		Detail: detail,
		Fix:    "forge-ci setup signer --principal \"$(git config user.email)\" --public-key " + publicKeyPath(key) + ", then commit and merge it",
	})
}

// signingPublicKey resolves git's user.signingkey: a key:: literal (or the
// older bare "ssh-…" literal), or a path to the public key or to the private
// key whose .pub sits beside it.
func signingPublicKey(key string) (string, error) {
	if literal, ok := strings.CutPrefix(key, "key::"); ok {
		return ParsePublicKey([]byte(literal), "user.signingkey")
	}

	if strings.HasPrefix(key, "ssh-") {
		return ParsePublicKey([]byte(key), "user.signingkey")
	}

	path := publicKeyPath(key)
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}

		path = filepath.Join(home, path[2:])
	}

	data, err := os.ReadFile(path) //nolint:gosec // the operator's configured public signing key; read-only.
	if err != nil {
		return "", fmt.Errorf("cannot read the public half of user.signingkey (%s): %w", path, err)
	}

	return ParsePublicKey(data, path)
}

func publicKeyPath(key string) string {
	if strings.HasPrefix(key, "key::") || strings.HasPrefix(key, "ssh-") || strings.HasSuffix(key, ".pub") {
		return key
	}

	return key + ".pub"
}

func tagChecks(ctx context.Context, git Git, request, release string) []RequestCheck {
	checks := make([]RequestCheck, 0, 2)

	for _, tag := range []string{request, release} {
		exists, err := git.TagExists(ctx, tag)

		check := RequestCheck{OK: err == nil && !exists, What: tag + " is unused", Detail: "no local tag " + tag}
		switch {
		case err != nil:
			check.Detail = "cannot list tags: " + err.Error()
		case exists && tag == release:
			check.Detail, check.Fix = release+" is already released", "pick the next version"
		case exists:
			check.Detail, check.Fix = "the request tag exists locally", "git tag -d "+tag+"   (if it was never pushed)"
		}

		checks = append(checks, check)
	}

	return checks
}

func valueOr(v, fallback string) string {
	if v == "" {
		return fallback
	}

	return v
}

// FormatRequest prints the checks, then the commands when they pass.
func FormatRequest(out io.Writer, plan RequestPlan) {
	_, _ = fmt.Fprintf(out, "Release request for %s\n\n", plan.Version)

	for _, c := range plan.Checks {
		mark := "[ok]  "
		if !c.OK {
			mark = "[todo]"
		}

		_, _ = fmt.Fprintf(out, "%s %s: %s\n", mark, c.What, c.Detail)

		if !c.OK && c.Fix != "" {
			_, _ = fmt.Fprintf(out, "       fix: %s\n", c.Fix)
		}
	}

	if !plan.Ready() {
		_, _ = fmt.Fprintln(out, "\nFix the [todo] lines, then run this again. The commands below would be refused by the release until then:")
	} else {
		_, _ = fmt.Fprintln(out, "\nFrom the merged default branch, up to date with the remote:")
	}

	for _, c := range plan.Commands {
		_, _ = fmt.Fprintf(out, "  %s\n", c)
	}

	_, _ = fmt.Fprintf(out, "\nThe release workflow then runs on the tag: it bumps the version, writes the changelog, builds, signs\n"+
		"and creates %s. Watch it in the forge's Actions or Pipelines page. If it fails: forge-ci docs failed-release-recovery\n", plan.Version)
}
