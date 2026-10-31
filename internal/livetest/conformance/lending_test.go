// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
)

func TestRunnerLending_LocalLifecycle(t *testing.T) {
	livetest.RegisterLiveScenario(t)
	target := livetest.Accept(t, provider.ForgeForgejo)
	for _, scenario := range []string{"job", "idle", "cancel"} {
		t.Run(scenario, func(t *testing.T) { livetest.CheckLocalLending(t, target, scenario) })
	}
}
