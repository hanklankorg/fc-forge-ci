// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package livetest

import (
 "fmt"
 "os"
 "testing"
 "github.com/diggsweden/forge-ci/internal/domain/provider"
)

func TestForgejoMigrationShellPullRequest(t *testing.T) {
 root:=os.Getenv("FORGE_CI_LIVE_MIGRATION_CHECKOUT")
 if root=="" {t.Skip("explicit read-only migration checkout required")}
 target:=Accept(t,provider.ForgeForgejo)
 engine:=NewBareScratchRepo(t,target,"migration-shell-engine","sha1",false)
 repo:=NewBareScratchRepo(t,target,"migration-shell-pr","sha1",false)
 sha,_,_:=nativeSnapshotTree(t,target,engine,snapshotCandidateFiles(t))
 nativeSnapshotPublishEngine(t,target,engine)
 consumer:=OpenConsumer(t,target,root)
 original:=consumer.PushSnapshot(t,repo)
 t.Logf("migration source snapshot=%s candidate=%s",original,sha)
 action:=target.BaseURL()+"/"+RepoSlug(target,engine)+"/.forgejo/actions/setup-forge-ci@"+sha
 workflow:=fmt.Sprintf(`name: Migration shell PR unit pilot
on:
  pull_request:
    branches: [main]
permissions:
  contents: read
jobs:
  units:
    runs-on: %s
    strategy:
      fail-fast: false
      matrix:
        include:
          - distro: ubuntu
            image: docker.io/library/ubuntu:26.04
          - distro: fedora
            image: docker.io/library/fedora:46@sha256:a9bab18d01cf2c2cf62f3e79c72623405bce14ac062995cc6651a3073c802e41
    container:
      image: ${{ matrix.image }}
    steps:
      - name: Original distro prerequisites
        shell: bash
        run: |
          set -euo pipefail
          if command -v apt-get >/dev/null; then
            apt-get update
            apt-get install -y git curl ca-certificates pipx fish jq gettext diffutils unzip xz-utils
          else
            dnf install -y git curl ca-certificates just pipx fish gettext jq diffutils unzip xz
          fi
      - uses: %s
        with:
          expected-sha256: 0e0f482ba8a6ea9be5c69ad7a508c67583fd86974cbd7125c158acd94e2d99e9
      - name: Actual pull request checkout through current engine
        shell: bash
        run: forge-ci platform checkout --depth 1
      - uses: https://github.com/jdx/mise-action@08ae2a091c038e1e05ec6f5115dc5c69d5a2a6b8
      - uses: https://github.com/extractions/setup-just@53165ef7e734c5c07cb06b3c8e7b647c5aa16db3
        with:
          just-version: 1.58.0
      - name: Original Bats setup and tests
        shell: bash
        run: |
          set -euo pipefail
          test "$(uname -m)" = x86_64
          just test-setup
          just test
`,RunnerLabel(t,target),action)
 CommitFiles(t,target,repo,FileCommit{Branch:"main",Message:"test: install scratch-only migration PR unit overlay",Files:map[string]string{".forgejo/workflows/migration-pr.yml":workflow}})
 feature:=CommitFiles(t,target,repo,FileCommit{Branch:"main",NewBranch:"migration-pr",Message:"test: exercise migration pull request",Files:map[string]string{consumerCanary:consumerCanaryText}})
 EnableActions(t,target,repo)
 pr:=OpenPullRequest(t,target,repo,"migration-pr","main","test: public migration shell unit pilot")
 t.Logf("migration PR opened: repository=%s number=%d head=%s",RepoSlug(target,repo),pr.Number,feature)
 run:=WaitForLayerRun(t,target,repo,"migration-pr","pull_request")
 t.Logf("migration PR result: run=%d status=%s",run.ID,run.Status)
 for _,job:=range RunJobs(t,target,repo,run.ID) {
  t.Logf("migration job: id=%d name=%s status=%s",job.ID,job.Name,job.Status)
  if len(job.RunsOn)==0 || job.Attempt==0 {continue}
  log,err:=jobLog(t,target,repo,job);if err!=nil {t.Errorf("runner log unavailable: job=%d",job.ID);continue}
  saveSnapshotEvidence(t,fmt.Sprintf("migration-pr-job-%d.log",job.ID),[]byte(redactTargetSecrets(target,log)))
 }
 if run.Status!="success" {t.Fatal("migration unit PR failed; retained original application check traces")}
}
