// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

func TestWorkflowWaitFatalFailureStillSettlesTheObservedRun(t *testing.T) {
	var cancellations atomic.Int32

	server, ca := labTLSServer(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")

		switch request.Method + " " + request.URL.Path {
		case "GET /api/v1/repos/fixture/fci-owned/actions/runs":
			_, _ = io.WriteString(response, `{"workflow_runs":[{"id":42,"workflow_id":"release.yml","event":"push","trigger_event":"push","status":"running"}]}`)
		case "GET /api/v1/repos/fixture/fci-owned/actions/runs/42":
			status := "running"
			if cancellations.Load() == 2 {
				status = "cancelled"
			}

			_, _ = io.WriteString(response, `{"id":42,"workflow_id":"release.yml","event":"push","status":"`+status+`"}`)
		case "GET /api/v1/repos/fixture/fci-owned/actions/runs/42/jobs":
			_, _ = io.WriteString(response, `[{"id":7,"run_id":42,"name":"prepare","status":"success"}]`)
		case "POST /api/v1/repos/fixture/fci-owned/actions/runs/42/cancel":
			if cancellations.Add(1) == 1 {
				response.WriteHeader(http.StatusBadRequest)
			} else {
				response.WriteHeader(http.StatusNoContent)
			}
		default:
			t.Errorf("unexpected workflow request: %s %s", request.Method, request.URL.Path)
			response.WriteHeader(http.StatusNotFound)
		}
	}))

	// This serial test redirects every cloned transport to its inert TLS server.
	// The disposable-host check and certificate validation remain active.
	original := http.DefaultTransport
	transport := original.(*http.Transport).Clone()
	transport.DialContext = (&dialRecorder{to: server.Listener.Addr().String()}).dial

	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = original }()
	defer transport.CloseIdleConnections()

	target := Target{Forge: provider.ForgeForgejo, Host: labHost, Owner: "fixture", CAFile: "captured", caPEM: ca, accepted: true}

	drain := runnerCancelDrain
	runnerCancelDrain = 0

	defer func() { runnerCancelDrain = drain }()

	func() {
		defer func() {
			failure, ok := recover().(acceptanceFailure)
			if !ok || !strings.Contains(string(failure), "cancel run 42 after job") {
				t.Errorf("primary wait failure was lost: %v", failure)
			}
		}()

		waitForRunOf(stopOnFatalTB{TB: t}, target, "fci-owned", "release.yml", "push", time.Minute, "prepare", 0)
		t.Error("refused cancellation returned a normal workflow conclusion")
	}()

	if cancellations.Load() != 2 {
		t.Fatalf("fatal wait failure left the observed run active: cancellations=%d", cancellations.Load())
	}
}

// The wait ends a run that can never end as a failure that names its blocked
// jobs, and cancels the run on its way out as any other failure does.
func TestWorkflowWaitFailsARunWhoseJobsAreAllBlockedOrOver(t *testing.T) {
	var cancelled atomic.Bool

	server, ca := labTLSServer(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")

		run, blocked := runBlocked, runBlocked
		if cancelled.Load() {
			run, blocked = runCancelled, runCancelled
		}

		switch request.Method + " " + request.URL.Path {
		case "GET /api/v1/repos/fixture/fci-owned/actions/runs":
			_, _ = io.WriteString(response, `{"workflow_runs":[{"id":42,"workflow_id":"snapshot.yml","event":"push","trigger_event":"push","status":"`+run+`"}]}`)
		case "GET /api/v1/repos/fixture/fci-owned/actions/runs/42":
			_, _ = io.WriteString(response, `{"id":42,"workflow_id":"snapshot.yml","event":"push","status":"`+run+`"}`)
		case "GET /api/v1/repos/fixture/fci-owned/actions/runs/42/jobs":
			_, _ = io.WriteString(response, `[{"id":7,"run_id":42,"name":"Plan","status":"success"},{"id":8,"run_id":42,"name":"Scan","status":"skipped"},`+
				`{"id":9,"run_id":42,"name":"Publish (incomplete matrix)","status":"`+blocked+`"},{"id":10,"run_id":42,"name":"Summary","status":"`+blocked+`"}]`)
		case "POST /api/v1/repos/fixture/fci-owned/actions/runs/42/cancel":
			cancelled.Store(true)
			response.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected workflow request: %s %s", request.Method, request.URL.Path)
			response.WriteHeader(http.StatusNotFound)
		}
	}))

	// As above: this serial test redirects every cloned transport to its
	// inert TLS server.
	original := http.DefaultTransport
	transport := original.(*http.Transport).Clone()
	transport.DialContext = (&dialRecorder{to: server.Listener.Addr().String()}).dial

	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = original }()
	defer transport.CloseIdleConnections()

	target := Target{Forge: provider.ForgeForgejo, Host: labHost, Owner: "fixture", CAFile: "captured", caPEM: ca, accepted: true}

	drain, settle := runnerCancelDrain, runDeadlockSettle
	runnerCancelDrain, runDeadlockSettle = 0, 0

	defer func() { runnerCancelDrain, runDeadlockSettle = drain, settle }()

	func() {
		defer func() {
			failure, _ := recover().(acceptanceFailure)
			if want := `run 42 of snapshot.yml can never end`; !strings.Contains(string(failure), want) ||
				!strings.Contains(string(failure), `"Publish (incomplete matrix)", "Summary"`) {
				t.Errorf("the wait ended with %q, want %q and the two blocked jobs", failure, want)
			}
		}()

		waitForRunOf(stopOnFatalTB{TB: t}, target, "fci-owned", "snapshot.yml", "push", time.Minute, "", 0)
		t.Error("a run that can never end was returned as over")
	}()

	if !cancelled.Load() {
		t.Error("the run was left uncancelled")
	}
}

func TestObservedWorkflowCancellationWaitsForJobsAndUsesOnlyObservedID(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		observed := WorkflowRun{ID: 226, WorkflowID: "release.yml", Event: "push", Status: "running"}
		cancelled, polls := false, 0
		check := func(id int64) {
			if id != observed.ID {
				t.Fatalf("unowned run %d", id)
			}
		}

		err := stopObservedWorkflowRun(t.Context(), observed, 0,
			func(_ context.Context, id int64) (WorkflowRun, error) {
				check(id)

				run := observed
				if cancelled {
					run.Status = "cancelled"
				}

				return run, nil
			},
			func(_ context.Context, id int64) error {
				check(id)

				cancelled = true

				return nil
			},
			func(_ context.Context, id int64) ([]RunJob, error) {
				check(id)

				polls++

				status := "running"
				if polls == 2 {
					status = "cancelled"
				}

				return []RunJob{{ID: 2944, RunID: id, Status: status}}, nil
			})
		if err != nil || !cancelled || polls != 2 {
			t.Fatalf("cancelled=%t polls=%d err=%v", cancelled, polls, err)
		}
	})
}

func TestObservedWorkflowCancellationRefusesIdentityDriftAndSkipsTerminalMutation(t *testing.T) {
	for _, drift := range []bool{false, true} {
		observed := WorkflowRun{ID: 226, WorkflowID: "release.yml", Event: "push"}

		err := stopObservedWorkflowRun(t.Context(), observed, 0,
			func(context.Context, int64) (WorkflowRun, error) {
				run := observed

				run.Status = "success"
				if drift {
					run.ID++
				}

				return run, nil
			},
			func(context.Context, int64) error {
				t.Fatal("terminal or unowned run cancelled")

				return nil
			},
			func(context.Context, int64) ([]RunJob, error) { return []RunJob{{RunID: 226, Status: "success"}}, nil })
		if (err != nil) != drift {
			t.Fatalf("drift=%t err=%v", drift, err)
		}
	}
}

func TestObservedWorkflowCancellationHasBoundedTerminalObservation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()

		observed := WorkflowRun{ID: 226, WorkflowID: "release.yml", Event: "push", Status: "running"}
		calls := 0

		err := stopObservedWorkflowRun(ctx, observed, 0,
			func(context.Context, int64) (WorkflowRun, error) { return observed, nil },
			func(context.Context, int64) error {
				calls++

				return nil
			},
			func(context.Context, int64) ([]RunJob, error) { return []RunJob{{RunID: 226, Status: "running"}}, nil })
		if !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
	})
}

func TestObservedWorkflowFailureSettlesIndependentJobWithoutChangingPrimaryConclusion(t *testing.T) {
	observed := WorkflowRun{ID: 228, WorkflowID: "release.yml", Event: "push", Status: "failure"}
	cancelled, observations := 0, 0

	err := stopObservedWorkflowRun(t.Context(), observed, 0,
		func(_ context.Context, id int64) (WorkflowRun, error) {
			if id != 228 {
				t.Fatalf("unowned run read: %d", id)
			}

			return observed, nil // The provider's primary failure remains failure.
		},
		func(_ context.Context, id int64) error {
			if id != 228 {
				t.Fatalf("unowned run cancelled: %d", id)
			}

			cancelled++

			return nil
		},
		func(_ context.Context, id int64) ([]RunJob, error) {
			observations++

			status := "running"
			if cancelled > 0 {
				status = "cancelled"
			}

			return []RunJob{{ID: 3030, RunID: id, Status: "failure"}, {ID: 3021, RunID: id, Status: status}, {ID: 3022, RunID: id, Status: "blocked"}}, nil
		})
	if err != nil || cancelled != 1 || observations != 2 || observed.Status != "failure" {
		t.Fatalf("cancelled=%d observations=%d primary=%s err=%v", cancelled, observations, observed.Status, err)
	}
}

// Forgejo answers a cancel with 500 "run has changed" while the run's runners
// report; giving up there left run 436 building after its repository went.
func TestObservedWorkflowCancellationAsksAgainWhenTheForgeFailsToAnswer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		observed := WorkflowRun{ID: 436, WorkflowID: "release.yml", Event: "release", Status: "failure"}
		attempts, cancelled := 0, false

		err := stopObservedWorkflowRun(t.Context(), observed, 0,
			func(context.Context, int64) (WorkflowRun, error) { return observed, nil },
			func(context.Context, int64) error {
				attempts++
				if attempts < 3 {
					return fmt.Errorf("POST cancel: HTTP 500: run has changed: %w", errServerSide)
				}

				cancelled = true

				return nil
			},
			func(_ context.Context, id int64) ([]RunJob, error) {
				status := "running"
				if cancelled {
					status = "cancelled"
				}

				return []RunJob{{ID: 5287, RunID: id, Status: status}}, nil
			})
		if err != nil || attempts != 3 || !cancelled {
			t.Fatalf("attempts=%d cancelled=%t err=%v", attempts, cancelled, err)
		}
	})
}

func TestObservedWorkflowCancellationKeepsAClientRefusalFinal(t *testing.T) {
	observed := WorkflowRun{ID: 436, WorkflowID: "release.yml", Event: "release", Status: "running"}
	attempts := 0

	err := stopObservedWorkflowRun(t.Context(), observed, 0,
		func(context.Context, int64) (WorkflowRun, error) { return observed, nil },
		func(context.Context, int64) error {
			attempts++

			return errors.New("POST cancel: HTTP 403")
		},
		func(_ context.Context, id int64) ([]RunJob, error) {
			return []RunJob{{ID: 5287, RunID: id, Status: "running"}}, nil
		})
	if err == nil || attempts != 1 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

// The forge shows a cancelled job finished before its runner has heard of
// the cancel, so a cancel is followed by the drain; a run that settled on its
// own needs none.
func TestObservedWorkflowCancellationDrainsOnlyAfterItCancelled(t *testing.T) {
	for _, running := range []bool{true, false} {
		synctest.Test(t, func(t *testing.T) {
			observed := WorkflowRun{ID: 436, WorkflowID: "release.yml", Event: "release", Status: "failure"}
			cancelled := false
			start := time.Now()

			err := stopObservedWorkflowRun(t.Context(), observed, time.Minute,
				func(context.Context, int64) (WorkflowRun, error) { return observed, nil },
				func(context.Context, int64) error {
					cancelled = true

					return nil
				},
				func(_ context.Context, id int64) ([]RunJob, error) {
					status := "failure"
					if running && !cancelled {
						status = "running"
					}

					return []RunJob{{ID: 5287, RunID: id, Status: status}}, nil
				})

			waited := time.Since(start)
			if err != nil || cancelled != running || (waited >= time.Minute) != running {
				t.Fatalf("running=%t cancelled=%t waited=%s err=%v", running, cancelled, waited, err)
			}
		})
	}
}

// A cancel whose answer failed (a proxy's 502) may still have been applied;
// the run then settles at the next poll and the runner still needs the drain.
func TestObservedWorkflowCancellationDrainsAfterAnUnansweredCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		observed := WorkflowRun{ID: 436, WorkflowID: "release.yml", Event: "release", Status: "running"}
		applied := false
		start := time.Now()

		err := stopObservedWorkflowRun(t.Context(), observed, time.Minute,
			func(context.Context, int64) (WorkflowRun, error) {
				if applied {
					return WorkflowRun{ID: 436, WorkflowID: "release.yml", Event: "release", Status: "cancelled"}, nil
				}

				return observed, nil
			},
			func(context.Context, int64) error {
				applied = true

				return fmt.Errorf("POST cancel: HTTP 502: %w", errServerSide)
			},
			func(_ context.Context, id int64) ([]RunJob, error) {
				status := "running"
				if applied {
					status = "cancelled"
				}

				return []RunJob{{ID: 5287, RunID: id, Status: status}}, nil
			})
		if waited := time.Since(start); err != nil || waited < time.Minute {
			t.Fatalf("waited=%s err=%v, want the drain after a cancel the forge applied", waited, err)
		}
	})
}
