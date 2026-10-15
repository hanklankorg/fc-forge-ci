// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import "github.com/diggsweden/forge-ci/internal/cli/regflags"

// Shared flag, subcommand, and credential-string names for the container
// package. Each name that repeats across command definitions is declared
// once here, so spellings cannot drift between verbs and the package stays
// under goconst's literal budget. Values are part of the CLI contract —
// docs/cli-reference.md is generated from them and a sync test gates any
// change. The registry-credential family aliases the shared regflags
// spellings so package-local reads stay in sync with the shared flag set.
const (
	flagAllowMissingLedger = "allow-missing-ledger"
	flagAnonymous          = "anonymous"
	flagArch               = "arch"
	flagAuthFile           = regflags.FlagAuthFile
	flagBaseInputID        = "base-input-id"
	flagBaseInputsJSON     = "base-inputs-json"
	flagContainerfile      = "containerfile"
	flagTitle              = "title"
	flagContext            = "context"
	// flagTempDir is the scratch-directory flag.
	flagTempDir                 = "temp-dir"
	flagExpectedBaseRepository  = "expected-base-repository"
	flagExpectedImageRepository = "expected-image-repository"
	flagExpectedSource          = "expected-source"
	flagExpectedWorkflow        = "expected-workflow"
	flagFile                    = "file"
	flagImage                   = "image"
	flagImageName               = "image-name"
	flagLedger                  = "ledger"
	flagName                    = "name"
	flagPlatform                = "platform"
	flagProvenanceEnvelope      = "provenance-envelope"
	flagRecursive               = "recursive"
	flagRef                     = "ref"
	flagRegistryPasswordFile    = regflags.FlagPasswordFile
	flagRegistryUsername        = regflags.FlagUsername
	flagRefName                 = "ref-name"
	flagRepository              = "repository"
	flagRetryAttempts           = "retry-attempts"
	flagRetryDelaySeconds       = "retry-delay-seconds"
	flagRevision                = "revision"
	flagSBOMPathPattern         = "sbom-path-pattern"
	flagSBOMSHA256              = "sbom-sha256"
	flagServerURL               = "server-url"
	flagSource                  = "source"
	flagSourceSHA               = "source-sha"
	flagStartedOn               = "started-on"
	flagTag                     = "tag"
	flagVersion                 = "version"
	flagWorkflow                = "workflow"
)

// Subcommand names shared by the image-lifecycle trees (ledger,
// release-images, base-images, signer-image): the same verb vocabulary is
// deliberately used in every tree.
const (
	subCmdCleanup  = "cleanup"
	subCmdImport   = "import"
	subCmdPromote  = "promote"
	subCmdRollback = "rollback"
	subCmdSign     = "sign"
	subCmdValidate = "validate"
)

// cosignMethodNoteGPG is the shared signflags method note for OCI-image
// signing verbs, where gpg cannot be a signing backend.
const cosignMethodNoteGPG = "gpg is rejected — it cannot sign OCI images."
