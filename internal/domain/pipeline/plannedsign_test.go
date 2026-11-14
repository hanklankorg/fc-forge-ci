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
		filesMethod     domainrelease.SignMethod
		image           bool
		idToken         bool
		importsGPG      bool
		signsContainers bool
		egress          bool
	}{
		{"empty defaults to gpg", "", "", false, false, true, false, false},
		{"gpg", domainrelease.SignMethodGPG, "", false, false, true, false, false},
		{"sigstore", domainrelease.SignMethodSigstore, "", false, true, false, true, true},
		{"kms", domainrelease.SignMethodKMS, "", false, false, false, true, true},
		// The files' gpg key is imported; the images keep the cosign method.
		{"gpg files beside sigstore images", domainrelease.SignMethodSigstore, domainrelease.SignMethodGPG, true, true, true, true, true},
		{"gpg files beside kms images", domainrelease.SignMethodKMS, domainrelease.SignMethodGPG, true, false, true, true, true},
		// Without an image the cosign method signs nothing: no token, no egress.
		{"gpg files and no image", domainrelease.SignMethodSigstore, domainrelease.SignMethodGPG, false, false, true, true, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := &config.Config{Sign: config.SignConfig{Method: tc.method, FilesMethod: tc.filesMethod}}
			if tc.image {
				cfg.Containers = []config.Container{{Name: "image"}}
			}

			plan := pipeline.NewConfigPlan(cfg)
			got := plan.Sign

			if got.FilesMethod != tc.filesMethod {
				t.Errorf("FilesMethod = %q, want %q", got.FilesMethod, tc.filesMethod)
			}

			if got.RequiresSigstoreEgress != tc.egress {
				t.Errorf("RequiresSigstoreEgress = %v, want %v", got.RequiresSigstoreEgress, tc.egress)
			}

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
