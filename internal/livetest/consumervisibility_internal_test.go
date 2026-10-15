// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

func TestConsumerVisibilityDefaultsToBothAndAllowsFocusedSelection(t *testing.T) {
	t.Parallel()

	public, private := ConsumerVisibility{Name: "public"}, ConsumerVisibility{Name: "private", Private: true}
	for _, test := range []struct {
		value string
		want  []ConsumerVisibility
	}{
		{"", []ConsumerVisibility{public, private}},
		{"both", []ConsumerVisibility{public, private}},
		{"public", []ConsumerVisibility{public}},
		{"private", []ConsumerVisibility{private}},
	} {
		actual, err := parseConsumerVisibilities(test.value)
		if err != nil || !reflect.DeepEqual(actual, test.want) {
			t.Fatalf("selection %q: %v, %v", test.value, actual, err)
		}
	}

	for _, value := range []string{"internal", "Private", "1", "private\n", "public,private"} {
		if _, err := parseConsumerVisibilities(value); !errors.Is(err, errs.ErrValidation) {
			t.Fatalf("invalid visibility selector %q was accepted", value)
		}
	}
}

func TestConsumerVisibilityRequiresExplicitProviderFacts(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		forge   provider.ForgeAPI
		body    string
		private bool
		want    bool
	}{
		{provider.ForgeForgejo, `{"id":42,"private":true}`, true, true},
		{provider.ForgeForgejo, `{"id":42,"private":false}`, false, true},
		{provider.ForgeForgejo, `{"id":42,"private":false}`, true, false},
		{provider.ForgeForgejo, `{"id":42,"visibility":"private"}`, true, false},
		{provider.ForgeForgejo, `{"id":42,"private":null,"visibility":"public"}`, false, false},
		{provider.ForgeForgejo, `{"private":true}`, true, false},
		{provider.ForgeGitLab, `{"id":42,"visibility":"private"}`, true, true},
		{provider.ForgeGitLab, `{"id":42,"visibility":"public"}`, false, true},
		{provider.ForgeGitLab, `{"id":42,"visibility":"internal"}`, true, false},
		{provider.ForgeGitLab, `{"id":42,"private":true}`, true, false},
		{provider.ForgeGitLab, `{"visibility":"private"}`, true, false},
	} {
		var state repositoryVisibilityState
		if err := json.Unmarshal([]byte(test.body), &state); err != nil {
			t.Fatal(err)
		}

		if got := state.matches(test.forge, test.private); got != test.want {
			t.Fatalf("%s %s: private=%t accepted=%t, want %t", test.forge, test.body, test.private, got, test.want)
		}
	}
}
