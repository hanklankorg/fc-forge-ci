// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

func TestRetainedScratchIsArchivedWithoutHidingActionsHistory(t *testing.T) {
	t.Parallel()

	for _, scenario := range []struct {
		name, calls string
		want        error
	}{
		{"retained", "GET PATCH GET", nil},
		{"absent", "GET", nil},
		{"already archived", "GET", nil},
		{"foreign identity", "GET", errs.ErrValidation},
		{"missing archive field", "GET", errs.ErrValidation},
		{"lookup denied", "GET", errs.ErrPermissionDenied},
		{"archive denied", "GET PATCH", errs.ErrPermissionDenied},
		{"archive disappeared", "GET PATCH", errs.ErrValidation},
		{"not archived", "GET PATCH GET", errs.ErrValidation},
		{"identity changed", "GET PATCH GET", errs.ErrValidation},
		{"readback denied", "GET PATCH GET", errs.ErrPermissionDenied},
		{"readback absent", "GET PATCH GET", errs.ErrValidation},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			request, calls := retainedScratchFixture(t, scenario.name)
			target := Target{accepted: true, Forge: provider.ForgeForgejo, Owner: "forgeci"}
			present, err := archiveRetainedScratch(target, "fci-consumer-trial", request)

			wantPresent := scenario.want == nil && scenario.name != "absent"
			if !errors.Is(err, scenario.want) || strings.Join(*calls, " ") != scenario.calls || present != wantPresent {
				t.Fatalf("error=%v calls=%v; want error=%v calls=%s", err, *calls, scenario.want, scenario.calls)
			}
		})
	}
}

func retainedScratchFixture(t *testing.T, scenario string) (retainedScratchRequest, *[]string) {
	t.Helper()

	var calls []string

	request := func(method string, body, into any) (int, error) {
		calls = append(calls, method)
		if method == http.MethodPatch {
			if !reflect.DeepEqual(body, map[string]bool{"archived": true}) {
				t.Fatalf("retention changed more than the archive flag: %v", body)
			}

			switch scenario {
			case "archive denied":
				return 403, errs.ErrPermissionDenied
			case "archive disappeared":
				return 404, nil
			default:
				return 200, nil
			}
		}

		if method != http.MethodGet {
			t.Fatalf("unexpected retention mutation %s", method)
		}

		readback := len(calls) > 1
		if scenario == "absent" || scenario == "readback absent" && readback {
			return 404, nil
		}

		if scenario == "lookup denied" || scenario == "readback denied" && readback {
			return 403, errs.ErrPermissionDenied
		}

		encoded, err := json.Marshal(retainedScratchReply(scenario, readback))
		if err != nil {
			t.Fatal(err)
		}

		return 200, json.Unmarshal(encoded, into)
	}

	return request, &calls
}

func retainedScratchReply(scenario string, readback bool) retainedScratch {
	state := retainedScratch{ID: 42, FullName: "forgeci/fci-consumer-trial", Archived: new(false)}
	if scenario == "foreign identity" {
		state.FullName = "someone/else"
	}

	if scenario == "already archived" || readback && scenario != "not archived" {
		state.Archived = new(true)
	}

	if scenario == "identity changed" && readback {
		state.ID++
	}

	if scenario == "missing archive field" {
		state.Archived = nil
	}

	return state
}

func TestRetainedScratchRefusesUnacceptedOrForeignCoordinates(t *testing.T) {
	t.Parallel()

	for _, scenario := range []struct {
		accepted bool
		forge    provider.ForgeAPI
		repo     string
	}{
		{false, provider.ForgeForgejo, "fci-example"},
		{true, provider.ForgeGitLab, "fci-example"},
		{true, provider.ForgeForgejo, "foreign"},
		{true, provider.ForgeForgejo, "fci-"},
		{true, provider.ForgeForgejo, "fci-../other"},
	} {
		_, err := archiveRetainedScratch(Target{accepted: scenario.accepted, Forge: scenario.forge}, scenario.repo,
			func(string, any, any) (int, error) {
				t.Fatal("an unaccepted retention coordinate reached the provider")

				return 0, nil
			})
		if !errors.Is(err, errs.ErrValidation) {
			t.Fatalf("invalid coordinate was admitted: %v", err)
		}
	}
}
