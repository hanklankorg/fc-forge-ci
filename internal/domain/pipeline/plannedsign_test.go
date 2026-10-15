// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline_test

import (
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
)

// TestPlanSign_PrecomputedFlags locks the semantic booleans the workflows read
// instead of comparing the method string, so a new method can't silently flip
// a workflow gate.
func TestPlanSign_PrecomputedFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		method          domainrelease.SignMethod
		idToken         bool
		importsGPG      bool
		signsContainers bool
	}{
		{"empty defaults to gpg", "", false, true, false},
		{"gpg", domainrelease.SignMethodGPG, false, true, false},
		{"sigstore", domainrelease.SignMethodSigstore, true, false, true},
		{"kms", domainrelease.SignMethodKMS, false, false, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			plan := pipeline.NewConfigPlan(&config.Config{Sign: config.SignConfig{Method: tc.method}})
			got := plan.Sign

			if got.RequiresIDToken != tc.idToken {
				t.Errorf("RequiresIDToken = %v, want %v", got.RequiresIDToken, tc.idToken)
			}

			if got.ImportsGPGKey != tc.importsGPG {
				t.Errorf("ImportsGPGKey = %v, want %v", got.ImportsGPGKey, tc.importsGPG)
			}

			if got.SignsContainers != tc.signsContainers {
				t.Errorf("SignsContainers = %v, want %v", got.SignsContainers, tc.signsContainers)
			}
		})
	}
}

// TestPlanSign_SigstoreEgressFollowsTheTransparencyChoice pins the egress
// flag the workflows use to decide whether a run may reach the public
// Sigstore services. kms with transparency none signs against the KMS alone
// and needs no egress; kms publishing to the log and keyless both do.
func TestPlanSign_SigstoreEgressFollowsTheTransparencyChoice(t *testing.T) {
	t.Parallel()

	for name, testCase := range map[string]struct {
		sign config.SignConfig
		want bool
	}{
		"kms none":    {sign: config.SignConfig{Method: domainrelease.SignMethodKMS, Transparency: domainrelease.TransparencyNone}, want: false},
		"kms public":  {sign: config.SignConfig{Method: domainrelease.SignMethodKMS, Transparency: domainrelease.TransparencyPublic}, want: true},
		"sigstore":    {sign: config.SignConfig{Method: domainrelease.SignMethodSigstore}, want: true},
		"gpg default": {sign: config.SignConfig{}, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			plan := pipeline.NewConfigPlan(&config.Config{Sign: testCase.sign})
			if plan.Sign.RequiresSigstoreEgress != testCase.want {
				t.Errorf("RequiresSigstoreEgress = %v, want %v (%+v)", plan.Sign.RequiresSigstoreEgress, testCase.want, plan.Sign)
			}
		})
	}
}

// TestPlanSign_UsesKeySecretOnlyForTheCosignKeySecret pins the flag that lets
// a signing step receive the Cosign key secret: kms with exactly that key
// reference, and nothing else, so a run that signs another way never holds it.
func TestPlanSign_UsesKeySecretOnlyForTheCosignKeySecret(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		sign config.SignConfig
		want bool
	}{
		{"cosign key secret", config.SignConfig{Method: domainrelease.SignMethodKMS, Key: config.CosignKeyEnvRef}, true},
		{"cloud kms", config.SignConfig{Method: domainrelease.SignMethodKMS, Key: "hashivault://transit/keys/release"}, false},
		{"keyless", config.SignConfig{Method: domainrelease.SignMethodSigstore}, false},
		{"gpg", config.SignConfig{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := pipeline.NewConfigPlan(&config.Config{Sign: tc.sign}).Sign.UsesKeySecret; got != tc.want {
				t.Errorf("UsesKeySecret = %v, want %v", got, tc.want)
			}
		})
	}
}
