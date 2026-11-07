// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	appbaseimages "github.com/diggsweden/forge-ci/internal/app/baseimages"
)

func TestParseBaseInputsForwardsNestedParentExpectation(t *testing.T) {
	t.Parallel()

	parent := &appbaseimages.BaseImageParent{Ref: "registry.example/shared/parent@sha256:" + strings.Repeat("b", 64), InputID: strings.Repeat("c", 64)}
	want := []appbaseimages.BaseInput{{Flavor: "child", BaseInputID: strings.Repeat("a", 64), ParentBase: parent}}

	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}

	got, err := parseBaseInputs(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	// This is the exact typed slice the validate action forwards to the app.
	in := appbaseimages.BaseImageVerifyExistingInput{BaseInputs: got}
	if !reflect.DeepEqual(in.BaseInputs, want) {
		t.Fatalf("forwarded inputs = %#v, want %#v", in.BaseInputs, want)
	}

	for _, raw := range []string{`[{"flavor":"child","parent_base":"bad"}]`, `[{"flavor":"child","parent_base":{"ref":42}}]`} {
		if _, err := parseBaseInputs(raw); err == nil {
			t.Fatalf("accepted malformed parent: %s", raw)
		}
	}
}
