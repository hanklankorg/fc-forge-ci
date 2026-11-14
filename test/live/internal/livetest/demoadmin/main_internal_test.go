// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

func TestParse_ReadsEachOperationAndRefusesTheRest(t *testing.T) {
	for name, test := range map[string]struct {
		args    []string
		want    request
		refused bool
	}{
		opList:                  {args: []string{opList, forgeForgejo}, want: request{operation: opList, forge: forgeForgejo}},
		opDelete:                {args: []string{opDelete, forgeGitLab, "service", "confirm=service"}, want: request{operation: opDelete, forge: forgeGitLab, name: "service", confirm: "service"}},
		"github list":           {args: []string{opList, forgeGitHub, "org=42"}, want: request{operation: opList, forge: forgeGitHub, org: 42}},
		"github without org":    {args: []string{opList, forgeGitHub}, refused: true},
		"org off GitHub":        {args: []string{opList, forgeForgejo, "org=42"}, refused: true},
		"delete without a name": {args: []string{opDelete, forgeForgejo}, refused: true},
		"confirm on a list":     {args: []string{opList, forgeForgejo, "confirm=x"}, refused: true},
		"unknown operation":     {args: []string{"purge", forgeForgejo}, refused: true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parse(test.args)
			if test.refused != (err != nil) || (!test.refused && got != test.want) {
				t.Fatalf("parse(%q) = %+v, %v", test.args, got, err)
			}
		})
	}
}

// fakeDemos records what a deletion was asked to remove.
type fakeDemos struct {
	sets    []livetest.DemoSet
	deleted []string
}

func (fake *fakeDemos) List(context.Context) ([]livetest.DemoSet, error) { return fake.sets, nil }

func (fake *fakeDemos) Delete(_ context.Context, set livetest.DemoSet) error {
	fake.deleted = append(fake.deleted, set.Name)
	fake.sets = nil

	return nil
}

// Nothing is deleted unless the demo's own name comes back, typed or given.
func TestRemove_DeletesOnlyWhatTheOperatorConfirms(t *testing.T) {
	sets := []livetest.DemoSet{{Name: "service", Repositories: []livetest.DemoRepository{{Name: "demo-service", ID: 1}}}}

	for name, test := range map[string]struct {
		in      request
		typed   string
		deleted bool
	}{
		"typed name":     {in: request{operation: opDelete, forge: forgeForgejo, name: "service"}, typed: "service\n", deleted: true},
		"confirm=":       {in: request{operation: opDelete, forge: forgeForgejo, name: "service", confirm: "service"}, deleted: true},
		"wrong name":     {in: request{operation: opDelete, forge: forgeForgejo, name: "service"}, typed: "other\n"},
		"nothing typed":  {in: request{operation: opDelete, forge: forgeForgejo, name: "service"}},
		"wrong confirm=": {in: request{operation: opDelete, forge: forgeForgejo, name: "service", confirm: "servic"}},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeDemos{sets: sets}

			var out bytes.Buffer

			err := remove(t.Context(), fake, sets, test.in, strings.NewReader(test.typed), &out)
			if test.deleted != (len(fake.deleted) == 1) {
				t.Fatalf("deleted %v, err %v, want deleted %v", fake.deleted, err, test.deleted)
			}

			if !test.deleted && !errors.Is(err, errAborted) {
				t.Fatalf("err = %v, want errAborted", err)
			}

			if !strings.Contains(out.String(), "demo-service") {
				t.Fatalf("the set was not shown before the question:\n%s", out.String())
			}
		})
	}
}

func TestRemove_RefusesAnUnknownDemo(t *testing.T) {
	fake := &fakeDemos{}

	err := remove(t.Context(), fake, nil, request{operation: opDelete, forge: forgeGitLab, name: "service", confirm: "service"}, strings.NewReader(""), &bytes.Buffer{})
	if err == nil || len(fake.deleted) != 0 {
		t.Fatalf("remove() = %v, deleted %v", err, fake.deleted)
	}
}
