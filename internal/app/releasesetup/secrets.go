// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package releasesetup

import (
	"fmt"
	"io"

	appdoctor "github.com/diggsweden/forge-ci/internal/app/doctor"
	"github.com/diggsweden/forge-ci/internal/domain/release"
)

// keyTools names the programs that make the signing keys: forge-ci stores
// and checks them, and does not generate key material itself.
const keyTools = "signing keys come from ssh-keygen (SSH), gpg (GPG) or cosign (key-based cosign)"

// Secret is one credential a release entry point needs, with how to store it.
type Secret struct {
	Name      string `json:"name"`
	State     string `json:"state"` // required, conditional
	Purpose   string `json:"purpose"`
	Authority string `json:"authority"`
	Scope     string `json:"scope"`
	Store     string `json:"store"`
	Workflow  string `json:"workflow"`
}

// SecretsReport lists the credentials to create, once per name, with the
// coverage caveats of the requirements scan they come from.
type SecretsReport struct {
	Secrets  []Secret `json:"secrets"`
	Unknowns []string `json:"unknowns"`
}

// Secrets reads the release entry points under root, like doctor
// --requirements, and keeps what a person has to create: the required and
// conditional credentials, each with the command that stores it. Credentials
// the forge provides itself are left out.
func Secrets(root, repo, commit string) SecretsReport {
	requirements := appdoctor.Requirements(root, repo, commit)
	report := SecretsReport{Secrets: []Secret{}, Unknowns: requirements.Unknowns}
	seen := map[string]bool{}

	for _, call := range requirements.Calls {
		for _, item := range call.Requirements {
			if item.State != release.SetupRequired && item.State != release.SetupConditional {
				continue
			}

			if seen[call.Forge+"/"+item.Name] {
				continue
			}

			seen[call.Forge+"/"+item.Name] = true
			report.Secrets = append(report.Secrets, Secret{
				Name: item.Name, State: string(item.State), Purpose: item.Purpose, Authority: item.Authority,
				Scope: item.Scope, Store: release.SecretStoreHint(call.Forge, item.Name), Workflow: call.Location,
			})
		}
	}

	return report
}

// FormatSecrets prints the secrets to create, required first.
func FormatSecrets(out io.Writer, report SecretsReport) {
	if len(report.Secrets) == 0 {
		_, _ = fmt.Fprintln(out, "No release entry point found that needs a secret you create.")
		_, _ = fmt.Fprintln(out, "forge-ci init writes the release workflow; run forge-ci setup secrets again after it.")
	}

	for _, state := range []string{string(release.SetupRequired), string(release.SetupConditional)} {
		for _, s := range report.Secrets {
			if s.State != state {
				continue
			}

			_, _ = fmt.Fprintf(out, "%s (%s)\n", s.Name, s.State)
			_, _ = fmt.Fprintf(out, "  for:    %s\n", s.Purpose)
			_, _ = fmt.Fprintf(out, "  needs:  %s\n", s.Authority)

			if s.Scope != "" {
				_, _ = fmt.Fprintf(out, "  used:   %s\n", s.Scope)
			}

			_, _ = fmt.Fprintf(out, "  store:  %s\n", s.Store)
			_, _ = fmt.Fprintf(out, "  in:     %s\n\n", s.Workflow)
		}
	}

	if len(report.Unknowns) > 0 {
		_, _ = fmt.Fprintln(out, "Not covered by this list:")

		for _, u := range report.Unknowns {
			_, _ = fmt.Fprintf(out, "  %s\n", u)
		}

		_, _ = fmt.Fprintln(out)
	}

	_, _ = fmt.Fprintln(out, "Conditional secrets depend on workflow choices; create them when the line says your setup uses them.")
	_, _ = fmt.Fprintln(out, "Signing keys: "+keyTools+"; forge-ci docs quickstart-setup-secrets walks through creating each one.")
}
