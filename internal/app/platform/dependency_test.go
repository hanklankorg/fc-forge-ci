// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package platform_test

import (
	"testing"

	appplatform "github.com/diggsweden/forge-ci/internal/app/platform"
	"github.com/stretchr/testify/require"
)

func TestDependencyRequiresUniqueSuccessfulProducerAndRequestedOutput(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"build", "outer.inner.build"} {
		value, err := appplatform.Dependency(appplatform.DependencyInput{NeedsJSON: `{"` + key + `":{"result":"success","outputs":{"images":"[]"}}}`, Job: "build", Output: "images"})
		require.NoError(t, err)
		require.Equal(t, "[]", value)
	}
	for _, body := range []string{
		`null`, `[]`, `{}`, `{} {}`, `{"build":null}`,
		`{"build":{"result":"success"}}`,
		`{"build":{"result":"success","outputs":{}}}`,
		`{"build":{"result":"success","outputs":{"images":""}}}`,
		`{"build":{"result":"success","outputs":{"images":[]}}}`,
		`{"build":{"result":"success","outputs":{"images":"a\nb"}}}`,
		`{"build":{"result":"failure","outputs":{"images":"[]"}}}`,
		`{"build":{"result":"cancelled","outputs":{"images":"[]"}}}`,
		`{"build":{"result":"skipped","outputs":{"images":"[]"}}}`,
		`{"build":{"result":"success"},"outer.build":{"result":"success"}}`,
		`{"one.build":{"result":"success"},"two.build":{"result":"success"}}`,
		`{"build":{},"build":{"result":"success","outputs":{"images":"[]"}}}`,
		`{"build":{"result":"failure","result":"success","outputs":{"images":"[]"}}}`,
		`{"build":{"result":"success","outputs":{"images":"bad","images":"[]"}}}`,
	} {
		_, err := appplatform.Dependency(appplatform.DependencyInput{NeedsJSON: body, Job: "build", Output: "images"})
		require.Error(t, err, body)
	}
}

func TestDependencyOptionalOutputDoesNotWaiveResultOrUniqueness(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{"promote":{"result":"skipped"}}`, `{"promote":{"result":"success","outputs":{}}}`, `{"promote":{"result":"success","outputs":{"images":null}}}`} {
		value, err := appplatform.Dependency(appplatform.DependencyInput{NeedsJSON: body, Job: "promote", Results: []string{"success", "skipped"}, Output: "images", AllowMissingOutput: true})
		require.NoError(t, err)
		require.Empty(t, value)
	}
	for _, body := range []string{`{}`, `{"promote":{"result":"failure"}}`, `{"promote":{"result":"cancelled"}}`, `{"promote":{"result":"success","outputs":[]}}`} {
		_, err := appplatform.Dependency(appplatform.DependencyInput{NeedsJSON: body, Job: "promote", Results: []string{"success", "skipped"}, Output: "images", AllowMissingOutput: true})
		require.Error(t, err)
	}
}

func TestDependencyObjectAndUsage(t *testing.T) {
	t.Parallel()
	value, err := appplatform.Dependency(appplatform.DependencyInput{NeedsJSON: `{"build":{"result":"success","outputs":{"images":"[]"}}}`, Job: "build"})
	require.NoError(t, err)
	require.JSONEq(t, `{"result":"success","outputs":{"images":"[]"}}`, value)
	for _, in := range []appplatform.DependencyInput{{Job: "outer.build"}, {Job: "build", Results: []string{"unknown"}}, {Job: "build", AllowMissingOutput: true}} {
		_, err := appplatform.Dependency(in)
		require.Error(t, err)
	}
}
