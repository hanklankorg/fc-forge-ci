// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"codefloe.com/itiquette/forge-lab-contract/labcontract"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// Same runner release as scripts/bootstrap/install-forgejo-runner.sh. The image
// supplies the real worker without running the root bootstrap on the host.
const lendingWorkerImage = "data.forgejo.org/forgejo/runner:13.2.1@sha256:568cea8c15ddc01fc8f330124892f572af7db37fa28344a2c1914405793bf61d"

const (
	lendingIdle = "idle"
	lendingJob  = "job"
)

var (
	errLendingObservation = errors.New("runner observation failed")
	errLendingOperation   = errors.New("local worker operation failed")
	errLendingTimeout     = errors.New("observation timed out")
)

type lendingRegistration struct {
	ID    int64  `json:"id"`
	UUID  string `json:"uuid"`
	Token string `json:"token"`
}

// lendingWorker holds only this invocation's coordinates and creation receipt.
type lendingWorker struct {
	target                              Target
	repo, name, base, podman, container string
	created                             lendingRegistration
}

func (worker *lendingWorker) run(ctx context.Context, input []byte, args ...string) (string, error) {
	return lendingPodman(ctx, worker.podman, input, args...)
}

// CheckLocalLending exercises the real ephemeral/one-job protocol. Provisioning
// is a local container, not a GitHub dispatch. Registration is repository-scoped
// and uses the accepted consumer credential, never lab administrator state.
func CheckLocalLending(tb TB, target Target, scenario string) {
	tb.Helper()
	requireAccepted(tb, target)

	if target.Forge != provider.ForgeForgejo || target.Owner != labAccount || labcontract.Disposable(target.Host) != nil {
		tb.Fatalf("local lending requires the accepted forgeci lab Forgejo target")

		return
	}

	if scenario != lendingJob && scenario != lendingIdle && scenario != "cancel" {
		tb.Fatalf("unknown lending case")

		return
	}

	ctx := Context(tb)
	podman := requireLendingEngine(tb, ctx)
	repo := NewScratchRepo(tb, target, "lending-"+scenario)
	EnableActions(tb, target, repo)
	worker := lendingWorker{
		target: target, repo: repo, name: repo + "-worker", podman: podman,
		base: target.BaseURL() + "/api/v1/repos/" + target.Owner + "/" + repo + "/actions/runners",
	}
	// Register cleanup before the POST; a lost response is reclaimed by the
	// scratch repository teardown, never a runner-name sweep.
	tb.Cleanup(func() {
		cleanup, cancel := cleanupContext(timeoutScratch)
		defer cancel()

		worker.cleanupContainer(tb, cleanup)
		worker.cleanupRegistration(tb, cleanup)
	})
	worker.register(tb, ctx, scenario)
	worker.start(tb, ctx)
	credential := worker.configure(tb, ctx)
	worker.observeOnline(tb, ctx)

	if scenario == lendingJob {
		worker.runJob(tb)
	} else {
		worker.interrupt(tb, ctx, scenario)

		if runs := WorkflowRuns(tb, target, repo); len(runs) != 0 {
			tb.Fatalf("local lending: idle worker unexpectedly received a workflow")
		}
	}

	worker.observeExit(tb, ctx)

	if scenario != lendingJob {
		worker.probeRetirementAuthority(tb, ctx, credential)
		worker.observeRegistrationLeftBehind(tb, ctx)

		return
	}

	worker.observeRetirement(tb, ctx)
}

func requireLendingEngine(tb TB, ctx context.Context) string {
	tb.Helper()

	podman, err := ResolveHostTool(ctx, "podman")
	if err != nil {
		tb.Fatalf("local lending requires rootless Podman: %v", err)
	}

	run := func(ctx context.Context, input []byte, args ...string) (string, error) {
		return lendingPodman(ctx, podman, input, args...)
	}

	rootless, err := run(ctx, nil, "info", "--format", "{{.Host.Security.Rootless}}")
	if err != nil || strings.TrimSpace(rootless) != "true" {
		tb.Fatalf("local lending requires the local rootless Podman engine")
	}

	if _, imageErr := run(ctx, nil, "image", "exists", lendingWorkerImage); imageErr != nil {
		tb.Fatalf("local lending requires the cached digest-pinned Forgejo 13.2.1 runner image")
	}

	return podman
}

func (worker *lendingWorker) cleanupContainer(tb TB, ctx context.Context) {
	if worker.container == "" {
		return
	}

	if _, err := worker.run(ctx, nil, "rm", "--force", "--volumes", worker.container); err != nil {
		tb.Errorf("local lending: owned container %s cleanup failed", worker.container)

		return
	}

	remaining, err := worker.run(ctx, nil, "ps", "--all", "--filter", "id="+worker.container, "--format", "{{.ID}}")
	if err != nil || strings.TrimSpace(remaining) != "" {
		tb.Errorf("local lending: container %s absence unproven", worker.container)

		return
	}

	tb.Logf("container=%s cleanup=absent", worker.container)
}

func (worker *lendingWorker) cleanupRegistration(tb TB, ctx context.Context) {
	id := worker.created.ID
	if id <= 0 {
		return
	}

	status, record, err := lendingRunner(ctx, worker.target, worker.base, id)
	if err != nil {
		tb.Errorf("local lending: runner %d cleanup observation failed", id)

		return
	}

	if status != http.StatusNotFound {
		if record.ID != id || record.Name != worker.name {
			tb.Errorf("local lending: runner identity changed; refusing deletion")

			return
		}

		tb.Logf("runner=%d fallback-revocation=true", id)

		if deleteErr := discard(ctx, worker.target, http.MethodDelete, worker.base+"/"+strconv.FormatInt(id, 10), nil, http.StatusNoContent, http.StatusOK); deleteErr != nil {
			tb.Errorf("local lending: exact runner cleanup failed")

			return
		}
	}

	status, _, err = lendingRunner(ctx, worker.target, worker.base, id)
	if err != nil || status != http.StatusNotFound {
		tb.Errorf("local lending: runner absence unproven")

		return
	}

	tb.Logf("runner=%d cleanup=absent", id)
}

func (worker *lendingWorker) register(tb TB, ctx context.Context, scenario string) {
	if _, err := decode(ctx, worker.target, http.MethodPost, worker.base, map[string]any{fieldName: worker.name, "ephemeral": true}, &worker.created, http.StatusCreated, http.StatusOK); err != nil {
		tb.Fatalf("local lending: repository-scoped ephemeral registration refused (requires Actions runner management on the owned scratch repository)")
	}

	if worker.created.ID <= 0 || worker.created.UUID == "" || worker.created.Token == "" {
		tb.Fatalf("local lending: incomplete registration receipt")
	}

	tb.Logf("case=%s runner=%s id=%d ephemeral=true provisioning=local-container", scenario, worker.name, worker.created.ID)
}

func (worker *lendingWorker) start(tb TB, ctx context.Context) {
	host, _, _ := strings.Cut(worker.target.Host, ":")
	container, err := worker.run(ctx, nil, "create", "--pull=never", "--name", worker.name,
		"--label", "forge-ci.live=lending", "--label", "forge-ci.repository="+worker.target.Owner+"/"+worker.repo,
		"--network", "host", "--add-host", host+":127.0.0.1",
		"--user", lendingWorkerUser, "--cap-drop=all", "--security-opt", "no-new-privileges",
		"--memory", "512m", "--cpus", "1", "--pids-limit", "128", "--workdir", "/tmp",
		"--env", "HOME=/tmp/worker", "--env", "SSL_CERT_FILE=/tmp/worker/ca.crt",
		"--entrypoint", "/bin/sh", lendingWorkerImage, "-c",
		"umask 077; mkdir /tmp/worker; cd /tmp/worker; while [ ! -f ready ]; do sleep 1; done; exec forgejo-runner one-job --wait --config config.yml")

	container = strings.TrimSpace(container)
	if err != nil || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(container) {
		tb.Fatalf("local lending: worker creation failed; inspect exact container name %s", worker.name)
	}

	worker.container = container

	if _, startErr := worker.run(ctx, nil, "start", container); startErr != nil {
		tb.Fatalf("local lending: worker start failed")
	}

	if pollErr := lendingPoll(ctx, 10*time.Second, func() (bool, error) {
		_, observeErr := worker.run(ctx, nil, "exec", container, "test", "-d", "/tmp/worker")

		return observeErr == nil, nil
	}); pollErr != nil {
		tb.Fatalf("local lending: worker filesystem not ready")
	}

	help, err := worker.run(ctx, nil, "exec", container, "forgejo-runner", "--help")
	if err != nil {
		tb.Fatalf("local lending: pinned runner interface inspection failed")
	}

	tb.Logf("pinned runner supported interfaces (before credential transfer):\n%s", help)
}

func (worker *lendingWorker) configure(tb TB, ctx context.Context) string {
	created, target, name := &worker.created, worker.target, worker.name

	registration, err := json.Marshal(map[string]any{"id": created.ID, "uuid": created.UUID, "name": name, "token": created.Token, "address": target.BaseURL(), "labels": []string{name + ":host"}, "ephemeral": true})
	if err != nil {
		tb.Fatalf("local lending: encode private registration")
	}

	workerCredential := created.Token
	created.Token = ""

	defer clear(registration)

	for _, file := range []struct {
		name string
		body []byte
	}{
		{".runner", registration}, {"ca.crt", target.caPEM},
		{"config.yml", []byte("log:\n  level: info\nrunner:\n  file: /tmp/worker/.runner\n  capacity: 1\n  timeout: 2m\n  fetch_interval: 1s\ncache:\n  enabled: false\nhost:\n  workdir_parent: /tmp/worker/work\n")},
		{"ready", []byte("ready\n")},
	} {
		if _, writeErr := worker.run(ctx, file.body, "exec", "-i", worker.container, "/bin/sh", "-c", "umask 077; cat > /tmp/worker/"+file.name); writeErr != nil {
			tb.Fatalf("local lending: private worker input failed")
		}
	}

	return workerCredential
}

func (worker *lendingWorker) observeOnline(tb TB, ctx context.Context) {
	if err := lendingPoll(ctx, 45*time.Second, func() (bool, error) {
		_, record, err := lendingRunner(ctx, worker.target, worker.base, worker.created.ID)

		return record.ID == worker.created.ID && record.Name == worker.name && (record.Status == lendingIdle || record.Status == "online"), err
	}); err != nil {
		tb.Fatalf("local lending: worker online proof failed: %v", err)
	}

	arch, err := worker.run(ctx, nil, "exec", worker.container, "uname", "-m")
	if err != nil {
		tb.Fatalf("local lending: architecture observation failed")
	}

	tb.Logf("runner=%d online=true architecture=%s profile=unprivileged-container-host-job", worker.created.ID, strings.TrimSpace(arch))
}

// lendingWorkerUser is the unprivileged UID:GID the lent worker runs as.
const lendingWorkerUser = "1000:1000"

// lendingJobWorkflow is the one job a lent runner takes. It checks it runs
// as the worker's user and group both: a check of the UID alone passes a job
// that kept a privileged group.
func lendingJobWorkflow(runner string) string {
	return fmt.Sprintf("on: [push]\njobs:\n  one:\n    runs-on: %s\n    steps:\n      - run: 'test \"$(id -u):$(id -g)\" = %s; uname -m; echo lending-one-job'\n",
		runner, lendingWorkerUser)
}

func (worker *lendingWorker) runJob(tb TB) {
	target, repo, name := worker.target, worker.repo, worker.name

	if conclusion := RunWorkflow(tb, target, repo, "lending", lendingJobWorkflow(name)); conclusion != runSuccess {
		tb.Fatalf("local lending job concluded %s", conclusion)
	}

	runs := WorkflowRuns(tb, target, repo)
	if len(runs) != 1 {
		tb.Fatalf("local lending: expected exactly one workflow run, got %d", len(runs))
	}

	jobs := RunJobs(tb, target, repo, runs[0].ID)
	if len(jobs) != 1 || jobs[0].Status != runSuccess || !slices.Equal(jobs[0].RunsOn, []string{name}) {
		tb.Fatalf("local lending: expected exactly one successful job on the unique worker label")
	}

	tb.Logf("runner=%d run=%d job=%d jobs=1 result=success", worker.created.ID, runs[0].ID, jobs[0].ID)
}

func (worker *lendingWorker) interrupt(tb TB, ctx context.Context, scenario string) {
	if scenario == lendingIdle {
		select {
		case <-ctx.Done():
			tb.Fatalf("local lending: idle wait cancelled")
		case <-time.After(2 * time.Minute):
		}
	}

	if _, err := worker.run(ctx, nil, "kill", "--signal", "INT", worker.container); err != nil {
		tb.Fatalf("local lending: worker interrupt failed")
	}

	tb.Logf("runner=%d signal=INT cause=%s", worker.created.ID, scenario)
}

func (worker *lendingWorker) observeExit(tb TB, ctx context.Context) {
	if err := lendingPoll(ctx, 40*time.Second, func() (bool, error) {
		state, err := worker.run(ctx, nil, "inspect", "--format", "{{.State.Status}}", worker.container)

		return strings.TrimSpace(state) == "exited", err
	}); err != nil {
		tb.Fatalf("local lending: worker did not exit: %v", err)
	}

	tb.Logf("runner=%d container=exited", worker.created.ID)
}

func (worker *lendingWorker) probeRetirementAuthority(tb TB, ctx context.Context, credential string) {
	// The documented REST deletion route is exercised only for this exact
	// owned registration. A runner credential is not a user management token.
	workerTarget := worker.target
	workerTarget.Token = credential

	status, err := decode(ctx, workerTarget, http.MethodDelete, worker.base+"/"+strconv.FormatInt(worker.created.ID, 10), nil, nil, http.StatusUnauthorized, http.StatusForbidden)
	if err != nil {
		tb.Fatalf("local lending: unexpected runner-credential retirement response (HTTP %d); inspect supported authority before changing the production contract", status)
	}

	tb.Logf("runner=%d REST-self-retirement=refused HTTP=%d credential=per-runner", worker.created.ID, status)
}

// A runner stopped before it took a job leaves its registration behind: the
// pinned runner retires itself only after a job, and has no way to retire
// otherwise (docs/runners.md, unused-runner retirement limitation). The
// cleanup then removes the exact registration as its owner and fails if it
// cannot. A runner that did retire itself fails here: the limitation is gone,
// and the guide and the lender are to follow.
func (worker *lendingWorker) observeRegistrationLeftBehind(tb TB, ctx context.Context) {
	err := lendingPoll(ctx, 15*time.Second, func() (bool, error) {
		status, _, err := lendingRunner(ctx, worker.target, worker.base, worker.created.ID)

		return status == http.StatusNotFound, err
	})
	if err == nil {
		tb.Fatalf("local lending: the unused runner retired itself; docs/runners.md says it cannot")
	}

	status, record, err := lendingRunner(ctx, worker.target, worker.base, worker.created.ID)
	if err != nil || status != http.StatusOK || record.ID != worker.created.ID || record.Name != worker.name {
		tb.Fatalf("local lending: the unused runner's registration could not be read back (HTTP %d): %v", status, err)
	}

	tb.Logf("runner=%d retired=false registration=present automatic=false", worker.created.ID)
}

func (worker *lendingWorker) observeRetirement(tb TB, ctx context.Context) {
	if err := lendingPoll(ctx, 15*time.Second, func() (bool, error) {
		status, _, err := lendingRunner(ctx, worker.target, worker.base, worker.created.ID)

		return status == http.StatusNotFound, err
	}); err != nil {
		tb.Fatalf("local lending: automatic deregistration not observed: %v", err)
	}

	tb.Logf("runner=%d retired=true registration=absent automatic=true", worker.created.ID)
}

type lendingRecord struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

func lendingRunner(ctx context.Context, target Target, base string, id int64) (int, lendingRecord, error) {
	var record lendingRecord

	status, err := decode(ctx, target, http.MethodGet, base+"/"+strconv.FormatInt(id, 10), nil, &record, http.StatusOK, http.StatusNotFound)
	if err != nil {
		return status, record, errLendingObservation
	}

	return status, record, nil
}

func lendingPodman(ctx context.Context, executable string, input []byte, args ...string) (string, error) {
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(call, executable, append([]string{"--remote=false"}, args...)...) //nolint:gosec // Resolved local Podman, fixed scenario argv and exact created IDs; credentials use stdin, never argv.
	cmd.Env = []string{"LC_ALL=C"}

	for _, key := range []string{"HOME", "PATH", "XDG_RUNTIME_DIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}

	cmd.Stdin = bytes.NewReader(input)
	cmd.Stderr = io.Discard // Never include worker credentials or provider bodies.

	output, err := cmd.Output()
	if err != nil {
		return "", errLendingOperation
	}

	return string(output), nil
}

func lendingPoll(ctx context.Context, budget time.Duration, observe func() (bool, error)) error {
	deadline := time.NewTimer(budget)
	defer deadline.Stop()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		done, err := observe()
		if done || err != nil {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errLendingTimeout
		case <-time.After(time.Second):
		}
	}
}
