// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
)

// Read an operator-selected existing private GHCR image; only the local lab
// repository is created. No image/package or hosted GitHub repository is changed.
func TestInRunner_PrivateGHCRJobImage(t *testing.T) {
	livetest.RegisterLiveScenario(t)
	image := os.Getenv("FORGE_CI_LIVE_PRIVATE_JOB_IMAGE")
	if image == "" {
		t.Skip("set FORGE_CI_LIVE_PRIVATE_JOB_IMAGE to an approved private GHCR digest reference")
	}

	ref, err := name.NewDigest(image, name.StrictValidation)
	if err != nil || ref.RegistryStr() != "ghcr.io" || ref.Name() != image {
		t.Fatal("private job-image probe requires a canonical ghcr.io repository@sha256 reference")
	}

	credential, present := livetest.ExternalRegistryCredential(t)
	if !present {
		t.Fatal("private job-image probe requires the external registry token-file and user settings")
	}

	for _, forge := range livetest.ForgesMeeting(t, []provider.ForgeAPI{provider.ForgeForgejo}, livetest.NeedsInRunner) {
		t.Run(string(forge), func(t *testing.T) {
			target := livetest.Accept(t, forge)
			arch := livetest.RunnerArch(t, target)
			requirePrivateGHCRImage(t, ref, credential, arch)
			repo := livetest.NewBareScratchRepo(t, target, "private-job-image", "sha1", true)
			livetest.CommitFiles(t, target, repo, livetest.FileCommit{
				Branch: "main", Message: "test: initialize private image pull probe",
				Files: map[string]string{"README.md": "Private job-image pull probe.\n"},
			})
			livetest.EnableActions(t, target, repo)
			livetest.SetRepoVariable(t, target, repo, "PULL_USER", credential.User)
			livetest.SetRepoSecret(t, target, repo, "PULL_TOKEN", credential.Token)

			labels, marshalErr := json.Marshal([]string{livetest.RunnerLabel(t, target)})
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}

			workflow := strings.NewReplacer("PROBE_LABELS", string(labels), "PROBE_REFERENCE", image, "PROBE_ARCH", arch).Replace(privateImageProbe)

			run := livetest.RunLayerWorkflow(t, target, repo, "private-job-image", workflow)

			jobs := livetest.RunJobs(t, target, repo, run.ID)
			if len(jobs) != 2 {
				t.Fatalf("expected planner and one smoke job, got %d jobs", len(jobs))
			}

			coldPull := false

			for _, job := range jobs {
				if job.Name == "plan" {
					continue
				}

				log := livetest.JobLog(t, target, repo, job)
				if job.Status != "success" {
					t.Log(livetest.JobLogTail(t, target, repo, job))
					t.Fatalf("private GHCR smoke job %d concluded %s", job.ID, job.Status)
				}

				pulled := strings.Contains(log, "docker pull image="+image+" ")
				// Runner 13.2 rejects empty container credentials before pulling.
				// Its info-level pull record identifies the explicitly supplied user;
				// internal debug messages depend on operator-owned log.job_level.
				authenticated := strings.Contains(log, "platform=linux/"+arch+" username="+credential.User+" ")
				executed := strings.Contains(log, "Private digest-pinned job image started successfully")
				t.Logf("smoke job %d %q: pull=%t supplied-user=%t executed=%t", job.ID, job.Name, pulled, authenticated, executed)
				coldPull = pulled && authenticated && executed
			}

			if run.Status != "success" || !coldPull {
				t.Fatalf("run %d concluded %s without a verified authenticated cold pull; select a private digest absent from the runner cache", run.ID, run.Status)
			}

			t.Logf("private GHCR digest %s was pulled with credentials and executed as a native %s job container in run %d", image, arch, run.ID)
		})
	}
}

func requirePrivateGHCRImage(t *testing.T, ref name.Digest, credential livetest.ExternalRegistry, arch string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	image, err := remote.Image(ref, remote.WithContext(ctx),
		remote.WithAuth(authn.FromConfig(authn.AuthConfig{Username: credential.User, Password: credential.Token})),
		remote.WithPlatform(v1.Platform{OS: "linux", Architecture: arch}))
	if err != nil {
		t.Fatalf("authenticated GHCR manifest read failed: %v", err)
	}

	config, err := image.ConfigFile()
	if err != nil {
		t.Fatalf("authenticated GHCR config read failed: %v", err)
	}

	if config.OS != "linux" || config.Architecture != arch {
		t.Fatalf("image platform %s/%s does not match native linux/%s", config.OS, config.Architecture, arch)
	}

	_, err = remote.Head(ref, remote.WithContext(ctx), remote.WithAuth(authn.Anonymous))

	var denied *transport.Error
	if !errors.As(err, &denied) || (denied.StatusCode != http.StatusUnauthorized && denied.StatusCode != http.StatusForbidden && denied.StatusCode != http.StatusNotFound) {
		t.Fatalf("expected anonymous refusal for the authenticated-existing image, got %v", err)
	}
}

// No credentials are embedded: the runner consumes the private repository's
// secret before starting the job container. The dynamic matrix matches runtime CI.
const privateImageProbe = `on: [push]
jobs:
  plan:
    runs-on: PROBE_LABELS
    outputs:
      matrix: ${{ steps.plan.outputs.matrix }}
    steps:
      - id: plan
        shell: sh
        env:
          IMAGE_REF: PROBE_REFERENCE
        run: printf 'matrix={"include":[{"ref":"%s","arch":"PROBE_ARCH"}]}\n' "$IMAGE_REF" >> "$GITHUB_OUTPUT"
  smoke:
    name: Private image (${{ matrix.arch }})
    needs: plan
    runs-on: PROBE_LABELS
    timeout-minutes: 5
    strategy:
      matrix: ${{ fromJSON(needs.plan.outputs.matrix) }}
    container:
      image: ${{ matrix.ref }}
      credentials:
        username: ${{ vars.PULL_USER }}
        password: ${{ secrets.PULL_TOKEN }}
    steps:
      - name: Execute inside the private image
        shell: sh
        run: |
          set -eu
          uname -m
          printf 'Private digest-pinned job image started successfully\n'
`
