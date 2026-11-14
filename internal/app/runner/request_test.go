// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package runner_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	apprunner "github.com/diggsweden/forge-ci/internal/app/runner"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/lender"
)

var errDispatchRefused = errors.New("workflow dispatch refused")

type recordingLender struct {
	requests []lender.Request
	err      error
}

func (l *recordingLender) Request(_ context.Context, req lender.Request) error {
	l.requests = append(l.requests, req)

	return l.err
}

func TestRequest_AsksTheLenderForOneRunnerOfTheArchitecture(t *testing.T) {
	t.Parallel()

	lend := &recordingLender{}

	var out strings.Builder

	err := apprunner.Request(t.Context(), lend, &out, apprunner.RequestInput{Arch: "arm64", TTLMinutes: 20, LenderName: "github:acme/lender"})
	if err != nil {
		t.Fatal(err)
	}

	if len(lend.requests) != 1 || lend.requests[0].Arch != lender.ArchARM64 || lend.requests[0].TTL != 20*time.Minute {
		t.Fatalf("requests=%+v, want one arm64 runner with a 20 minute ttl", lend.requests)
	}

	for _, want := range []string{"linux/arm64", "github:acme/lender", "20 minutes"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("summary %q lacks %q", out.String(), want)
		}
	}
}

// The installation with its own container-capable runners configures no
// lender; the request job still runs so the workflow shape is one, and it
// says where the container job runs instead.
func TestRequest_WithoutALenderRequestsNothingAndSaysSo(t *testing.T) {
	t.Parallel()

	var out strings.Builder

	err := apprunner.Request(t.Context(), nil, &out, apprunner.RequestInput{Arch: "amd64", TTLMinutes: 20})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), "No runner lender is configured") || !strings.Contains(out.String(), "CI_RUNNER_LABEL_CONTAINER") {
		t.Errorf("summary %q must name the variable the container job runs by", out.String())
	}
}

func TestRequest_RefusesBadInputBeforeAskingTheLender(t *testing.T) {
	t.Parallel()

	for name, in := range map[string]apprunner.RequestInput{
		"unknown architecture": {Arch: "riscv64", TTLMinutes: 20, LenderName: "github:acme/lender"},
		"ttl too short":        {Arch: "amd64", TTLMinutes: 1, LenderName: "github:acme/lender"},
		"ttl too long":         {Arch: "amd64", TTLMinutes: 181, LenderName: "github:acme/lender"},
		"nameless lender":      {Arch: "amd64", TTLMinutes: 20},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			lend := &recordingLender{}

			err := apprunner.Request(t.Context(), lend, &strings.Builder{}, in)
			if !errors.Is(err, errs.ErrUsage) {
				t.Fatalf("err=%v, want ErrUsage", err)
			}

			if len(lend.requests) != 0 {
				t.Errorf("the lender was asked despite invalid input: %+v", lend.requests)
			}
		})
	}
}

func TestRequest_WrapsTheLendersFailureWithItsName(t *testing.T) {
	t.Parallel()

	lend := &recordingLender{err: errDispatchRefused}

	err := apprunner.Request(t.Context(), lend, &strings.Builder{}, apprunner.RequestInput{Arch: "amd64", TTLMinutes: 20, LenderName: "github:acme/lender"})
	if !errors.Is(err, errDispatchRefused) || !strings.Contains(err.Error(), "github:acme/lender") {
		t.Fatalf("err=%v, want the lender's failure named after the lender", err)
	}
}
