// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

// The signer confines its ledger to the repository the verifier looks in. Both
// verbs get it from baseImagesExpectedRepository, so the same flags name the
// same repository on either side.
func TestBaseImagesSign_ExpectedRepositoryIsTheOneValidateDerives(t *testing.T) {
	t.Parallel()

	resolved := func(cmd *cli.Command, resolve func(*cli.Command) (baseImagesCommon, error), args ...string) string {
		var common baseImagesCommon

		cmd.Action = func(_ context.Context, parsed *cli.Command) error {
			var err error

			common, err = resolve(parsed)

			return err
		}

		require.NoError(t, cmd.Run(t.Context(), append([]string{cmd.Name}, args...)))

		return common.ExpectedRepository
	}

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "derived from the forge host, the lowercased repository and the suffix",
			args: []string{"--server-url", "https://forge.example/", "--repository", "Owner/App", "--repository-suffix", "-base"},
			want: "forge.example/owner/app-base",
		},
		{
			name: "named outright",
			args: []string{"--server-url", "https://forge.example", "--repository", "Owner/App", "--expected-repository", "forge.example/owner/bases"},
			want: "forge.example/owner/bases",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			validate := resolved(baseImagesVerifyCmd(),
				func(cmd *cli.Command) (baseImagesCommon, error) { return baseImagesCommonFromCmd(cmd, false, true) },
				append(tc.args, "--expected-workflow", "build.yml", "--cosign-public-key-path", "keys/cosign.pub")...)
			sign := resolved(baseImagesSignCmd(),
				func(cmd *cli.Command) (baseImagesCommon, error) { return baseImagesLocationFromCmd(cmd, true) },
				append(tc.args, "--ledger", "unread.json")...)

			require.Equal(t, tc.want, validate)
			require.Equal(t, validate, sign)
		})
	}
}

// Every refusal below is decided from the flags and the ledger alone. None of
// them reaches the registry, runs cosign, or reads the registry password: the
// password file named here does not exist, so reading it would surface as a
// different error.
func TestBaseImagesSign_RefusesBeforeAnyRegistryOrSigningEffect(t *testing.T) {
	const (
		digest  = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		inputID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		commit  = "cccccccccccccccccccccccccccccccccccccccc"
	)

	entryUnder := func(repository string) imageledger.Entry {
		return imageledger.Entry{
			Role: "base", ImageKind: imageledger.ImageKindBase, Flavor: "alpine",
			Ref: repository + "@" + digest, Digest: digest,
			FinalTag: repository + ":" + inputID + "-alpine", BaseInputID: inputID,
			SBOM: "premade-sboms/base-sbom-alpine.cyclonedx.json",
		}
	}

	for _, tc := range []struct {
		name     string
		ledger   func(repository string) []byte
		args     map[string]string
		env      map[string]string // a flag set to "" here is left off the command line
		located  bool              // the registry and the repository are named outright
		want     error
		contains string
	}{
		{
			name:   "malformed ledger",
			ledger: func(string) []byte { return []byte(`[{"ref":`) },
			want:   errs.ErrMalformedInput, contains: "imageledger: parse ledger",
		},
		{
			name: "entry outside the expected repository",
			ledger: func(repository string) []byte {
				return marshalLedger(t, entryUnder(strings.TrimSuffix(repository, "-base")))
			},
			want: errs.ErrValidation, contains: "must be under",
		},
		{
			name: "entry that is not a base image",
			ledger: func(repository string) []byte {
				entry := entryUnder(repository)
				entry.ImageKind = imageledger.ImageKindRelease

				return marshalLedger(t, entry)
			},
			want: errs.ErrValidation, contains: "image_kind",
		},
		{name: "unsafe workflow", args: map[string]string{"--workflow": "../../build.yml"}, want: errs.ErrUsage, contains: "--workflow must be a relative workflow path"},
		{name: "no workflow", args: map[string]string{"--workflow": ""}, want: errs.ErrUsage, contains: "workflow is required"},
		{name: "malformed commit", args: map[string]string{"--source-sha": "not-a-commit"}, want: errs.ErrValidation, contains: "commit SHA"},
		{name: "bad repository suffix", args: map[string]string{"--repository-suffix": "/other"}, want: errs.ErrUsage, contains: "repository-suffix must be empty"},
		{name: "timestamp that is not RFC3339", args: map[string]string{"--started-on": "yesterday"}, want: errs.ErrUsage, contains: "is not RFC3339"},
		{name: "unknown signing method", args: map[string]string{"--method": "pgp"}, contains: "pgp"},
		{
			// The ledger also comes from its variable.
			name: "malformed ledger named by BASE_IMAGES_LEDGER", env: map[string]string{"--ledger": "", "BASE_IMAGES_LEDGER": "malformed.ledger.json"},
			want: errs.ErrMalformedInput, contains: "imageledger: parse ledger",
		},
		{
			// What the statement records comes from the command line: a
			// SOURCE_SHA the job happens to carry names no commit.
			name: "no commit flag beside an ambient SOURCE_SHA", env: map[string]string{"--source-sha": "", "SOURCE_SHA": commit},
			want: errs.ErrValidation, contains: "commit SHA",
		},
		{
			// The provenance records the source, and the signer has no
			// --expected-source to name it with.
			name: "no server URL to name the source", located: true, args: map[string]string{"--server-url": ""},
			want: errs.ErrUsage, contains: "--server-url and --repository are required: they name the source repository",
		},
		{
			name: "no repository to name the source", located: true, args: map[string]string{"--repository": ""},
			want: errs.ErrUsage, contains: "--server-url and --repository are required: they name the source repository",
		},
		{
			// Refused from the flags, before the ledger is read from stdin.
			name: "ledger and registry password both from stdin",
			args: map[string]string{"--ledger": "-", "--registry-password-file": "-"},
			want: errs.ErrUsage, contains: "--ledger and --registry-password-file both read stdin",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := testenv.New(t)
			t.Chdir(env.Temp)

			for key, value := range map[string]string{
				"GITHUB_ACTIONS": "true", "FORGEJO_ACTIONS": "true", "FORGEJO_RUN_ID": "7", "FORGEJO_REF_NAME": "main",
				"RUNNER_TEMP": env.MkdirAll("runner-temp"), "COSIGN_KEY": "fixture-signing-key",
			} {
				t.Setenv(key, value)
			}

			registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("registry contacted before the refusal: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			t.Cleanup(registry.Close)

			mock := mockbinary.New(t)
			mock.OnPath()
			mock.Add("cosign", "")

			repository := strings.TrimPrefix(registry.URL, "http://") + "/owner/app-base"

			ledger := marshalLedger(t, entryUnder(repository))
			if tc.ledger != nil {
				ledger = tc.ledger(repository)
			}

			require.NoError(t, os.WriteFile(env.Path("base-images.ledger.json"), ledger, 0o600))
			require.NoError(t, os.WriteFile(env.Path("malformed.ledger.json"), []byte(`[{"ref":`), 0o600))

			args := map[string]string{
				"--ledger": "base-images.ledger.json", "--server-url": registry.URL, "--repository": "Owner/App",
				"--repository-suffix": "-base", "--workflow": "build-base-images.yml", "--source-sha": commit,
				"--started-on": "2026-01-02T03:04:05Z", "--method": "kms", "--key": "env://COSIGN_KEY",
				"--registry-username": "fixture", "--registry-password-file": env.Path("absent-password"),
			}
			if tc.located {
				args["--registry"], args["--expected-repository"] = strings.TrimPrefix(registry.URL, "http://"), repository
			}

			for flag, value := range tc.args {
				args[flag] = value
			}

			for name, value := range tc.env {
				if strings.HasPrefix(name, "--") {
					delete(args, name)
				} else {
					t.Setenv(name, value)
				}
			}

			cmd := baseImagesSignCmd()

			argv := make([]string, 0, 1+2*len(args))
			argv = append(argv, cmd.Name)

			for flag, value := range args {
				argv = append(argv, flag, value)
			}

			err := cmd.Run(t.Context(), argv)
			require.ErrorContains(t, err, tc.contains)

			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
			}

			require.Empty(t, mock.Invocations("cosign"))

			scratch, readErr := os.ReadDir(env.Path("runner-temp"))
			require.NoError(t, readErr)
			require.Empty(t, scratch, "a refusal left registry auth scratch behind")
		})
	}
}

func marshalLedger(t *testing.T, entries ...imageledger.Entry) []byte {
	t.Helper()

	body, err := imageledger.Marshal(entries)
	require.NoError(t, err)

	return body
}
