// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

// The version group's one verb with authority: commit-changelog-release signs
// a commit, pushes a branch and creates a tag nobody may move afterwards.
//
// It runs inside a job because that is the only place it can. The push goes
// over SSH to an address the contract publishes for runners, which the host
// does not reach by that name; and the command reads the run it is in for
// everything it is not told. So the job makes the release and the host judges
// it afterwards from a mirror, with git and the deploy key's public half:
// what a consumer of the release can check, and nothing the job reported.
//
// Forgejo alone. The kit registers a deploy key, sets a repository secret and
// knows a runner-side SSH route for Forgejo only; the GitLab release path
// (.gitlab/bin/release.sh) has no scenario here.

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"

	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

func TestInRunner_VersionCommitsTheChangelogRelease(t *testing.T) {
	livetest.RegisterLiveScenario(t)

	const (
		name     = "inrunner-version"
		assetTag = "v0.0.7-version"
	)

	if !livetest.Requires(t, provider.ForgeForgejo, livetest.NeedsInRunner) {
		livetest.NotCoveredf(t, "the changelog release needs a Forgejo runner")
	}

	target := livetest.Accept(t, provider.ForgeForgejo)

	gitURL, fingerprint, ok := livetest.SSHRoute(t, target, livetest.ScratchRepo(name))
	if !ok {
		t.Skip("no SSH route, so the release commit cannot be pushed")
	}

	// One repository holds the binary's release and is the one released
	// from, as the neighbouring probes keep theirs; the job reads both
	// without a credential.
	repo := livetest.NewPublicScratchRepo(t, target, name, livetest.ReadProbeAssetAnonymously)

	livetest.PrepareTag(t, target, repo, assetTag)
	livetest.PublishBinaryAsset(t, target, repo, assetTag, t.TempDir())

	prelude := livetest.ProbePrelude(target, livetest.ReleaseAssetURL(t, target, repo, assetTag, "forge-ci"))

	keyDir := t.TempDir()
	private, public := livetest.SSHKey(t, keyDir)
	livetest.AddDeployKey(t, target, repo, "livetest version probe", public)
	livetest.SetRepoSecret(t, target, repo, versionSigningKeySecret, private)

	release := versionRelease{Repository: livetest.RepoSlug(target, repo), GitURL: gitURL, Fingerprint: fingerprint}

	// Both workflows in one commit, which is the head every release below
	// starts from. Neither runs yet: each waits for its own tag.
	command := []string{"version", "commit-changelog-release"}
	request := livetest.CommitFiles(t, target, repo, livetest.FileCommit{
		Branch: layerMain, Message: "test: seed the version probe",
		Files: map[string]string{
			"CHANGELOG.md": "# Changelog\n",
			".forgejo/workflows/" + versionDryRunWorkflow + ".yml":  versionWorkflow(prelude, release, command, true),
			".forgejo/workflows/" + versionReleaseWorkflow + ".yml": versionWorkflow(prelude, release, command, false),
		},
	})

	logs := layerScenario{target: target, repo: repo}
	seeded := versionRefs(t, target, repo)

	// The preview holds the key and a route, so leaving the remote alone is
	// the command's doing and not the job's inability.
	livetest.AnnotateTag(t, target, repo, versionTrigger(versionDryRunWorkflow), layerMain, "Preview release "+versionReleaseTag)

	preview := livetest.WaitForLayerRun(t, target, repo, versionDryRunWorkflow, "push")
	requireVersionJobLogs(t, logs, preview.ID, private)

	if preview.Status != jobSuccess {
		t.Errorf("the dry run ended %q, so it did not narrate the release it would make", preview.Status)
	}

	previewed := versionRefs(t, target, repo)
	if _, tagged := previewed["refs/tags/"+versionTrigger(versionDryRunWorkflow)]; !tagged {
		t.Fatal("the mirror does not hold the tag that started the dry run, so it cannot show what the dry run left")
	}

	delete(previewed, "refs/tags/"+versionTrigger(versionDryRunWorkflow))

	if !maps.Equal(previewed, seeded) {
		t.Fatalf("the dry run changed the remote: refs are %v, want %v", previewed, seeded)
	}

	livetest.AnnotateTag(t, target, repo, versionTrigger(versionReleaseWorkflow), layerMain, "Release "+versionReleaseTag)

	released := livetest.WaitForLayerRun(t, target, repo, versionReleaseWorkflow, "push")
	requireVersionJobLogs(t, logs, released.ID, private)

	if released.Status != jobSuccess {
		t.Errorf("the release ended %q", released.Status)
	}

	requireVersionRelease(t, target, repo, request, public, slices.Sorted(maps.Keys(seeded)))
}

// versionRefs maps every branch and tag of the scratch repository to the
// object it names, read from a fresh mirror.
func versionRefs(t *testing.T, target livetest.Target, repo string) map[string]string {
	t.Helper()

	listed, err := livetest.MirrorRepo(t, target, repo).Git(t, "for-each-ref", "--format=%(refname) %(objectname)")
	if err != nil {
		t.Fatalf("list the refs: %v", err)
	}

	refs := map[string]string{}

	for line := range strings.SplitSeq(listed, "\n") {
		if name, object, ok := strings.Cut(line, " "); ok {
			refs[name] = object
		}
	}

	return refs
}

// requireVersionRelease asks a mirror what the release did to the repository:
// one commit on main over request, holding the changelog alone under the
// message the job supplied, the final tag on it, both signed by the deploy
// key, and no other ref made or moved.
func requireVersionRelease(t *testing.T, target livetest.Target, repo, request, deployKey string, seeded []string) {
	t.Helper()

	mirror := livetest.MirrorRepo(t, target, repo)
	git := func(args ...string) string {
		out, err := mirror.Git(t, args...)
		if err != nil {
			t.Fatalf("release history: %v", err)
		}

		return out
	}

	main := git("rev-parse", "refs/heads/"+layerMain)
	if parents := git("rev-list", "--parents", "-n", "1", main); parents != main+" "+request || git("rev-list", "--count", request+".."+main) != "1" {
		t.Fatalf("main is at %q, want one release commit whose only parent is the seeded head %s", parents, request)
	}

	// commit-msg.txt, the unrelated file and the product's own binary and
	// output all sat in the working tree; the commit holds none of them.
	if changed := git("diff-tree", "--no-commit-id", "--name-only", "-r", request, main); changed != "CHANGELOG.md" {
		t.Errorf("the release commit changed %q, want CHANGELOG.md alone", changed)
	}

	if changelog := git("show", main+":CHANGELOG.md"); changelog != strings.TrimSpace(versionChangelog) {
		t.Errorf("the released changelog is %q, want %q", changelog, versionChangelog)
	}

	// The message is the supplied file and the sign-off the command adds.
	identity := versionAuthorName + " <" + versionAuthorEmail + ">"
	if message, want := git("log", "-1", "--format=%B", main), strings.TrimSpace(versionCommitMessage)+"\n\nSigned-off-by: "+identity; message != want {
		t.Errorf("the release commit says %q, want %q", message, want)
	}

	if people := git("log", "-1", "--format=%an <%ae>|%cn <%ce>", main); people != identity+"|"+identity {
		t.Errorf("the release commit is by %q, want %s as author and committer", people, identity)
	}

	final := "refs/tags/" + versionReleaseTag
	if tagged := git("rev-parse", final+"^{commit}"); tagged != main {
		t.Errorf("%s points at %s, want the release commit %s", versionReleaseTag, tagged, main)
	}

	if kind := git("cat-file", "-t", final); kind != "tag" {
		t.Errorf("%s is a %s, want a tag object, which is what carries a signature", versionReleaseTag, kind)
	}

	requireVersionSignatures(t, mirror, deployKey, main, versionReleaseTag)

	// The two tags that started the runs stay where the host put them, and
	// the final tag is the only ref the release added.
	triggers := []string{"refs/tags/" + versionTrigger(versionDryRunWorkflow), "refs/tags/" + versionTrigger(versionReleaseWorkflow)}
	for _, trigger := range triggers {
		if at := git("rev-parse", trigger+"^{commit}"); at != request {
			t.Errorf("%s moved to %s, want it left at %s", trigger, at, request)
		}
	}

	want := slices.Sorted(slices.Values(append(append([]string{final}, triggers...), seeded...)))
	if refs := slices.Sorted(strings.SplitSeq(git("for-each-ref", "--format=%(refname)"), "\n")); !slices.Equal(refs, want) {
		t.Errorf("the repository holds the refs %v, want %v", refs, want)
	}
}

// requireVersionSignatures holds the commit and the tag to the deploy key,
// by the release owner's own oracle: the key's fingerprint, and a control file
// naming a key made a moment ago, against which both must fail.
func requireVersionSignatures(t *testing.T, mirror livetest.RepoMirror, deployKey, commit, tag string) {
	t.Helper()

	_, strangerKey := livetest.SSHKey(t, t.TempDir())
	dir := t.TempDir()
	trust := releaseSignerTrust{allowed: filepath.Join(dir, "allowed"), foreign: filepath.Join(dir, "foreign"), publicKey: deployKey}

	// The principal is only a name for the one key in each file.
	for file, key := range map[string]string{trust.allowed: deployKey, trust.foreign: strangerKey} {
		if err := os.WriteFile(file, []byte(versionAuthorEmail+" "+key+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	git := func(args ...string) (string, error) { return mirror.Git(t, args...) }
	for _, finding := range releaseSignerFindings(git, trust, commit, tag) {
		t.Error(finding)
	}
}

// requireVersionJobLogs retains a run's job logs and holds each to no line of
// the signing key, which the dry-run job writes to a file of its own.
func requireVersionJobLogs(t *testing.T, logs layerScenario, runID int64, private string) {
	t.Helper()

	jobs := livetest.RunJobs(t, logs.target, logs.repo, runID)
	logs.logJobs(t, jobs, map[string]string{"SSH signing key": private})

	for _, job := range jobs {
		requireNoReleaseKey(t, job.Name, livetest.JobLog(t, logs.target, logs.repo, job), releaseKeys{sshPrivate: private})
	}
}
