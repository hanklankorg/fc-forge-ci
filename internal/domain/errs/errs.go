// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package errs holds the small surface main() needs to translate
// errors into process exit codes:
//
//   - ExitCodeType + sysexits.h-aligned constants
//   - ExitCodeFromError(err) — the one mapping main() applies
//   - A couple of sentinels callers branch on with errors.Is
//
// Domain and app code create errors with plain `fmt.Errorf("...: %w",
// err)`. There is no typed-error builder; if future requirements need
// structured exit codes per failure class, add a sentinel + a case in
// ExitCodeFromError. Don't reach for a framework until that demand is
// concrete.
package errs

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/url"
	"syscall"
)

// Sentinels callers wrap with `fmt.Errorf("ctx: %w", sentinel)` so
// errors.Is keeps working and ExitCodeFromError can map each shape to
// a sysexits.h exit code. Each sentinel corresponds to one ExitCode*
// value; the mapping lives in ExitCodeFromError below.
//
// Wrap pattern (stdlib-aligned, like fs.ErrPermission):
//
//	return fmt.Errorf("commit-push: BRANCH is required: %w", errs.ErrUsage)
//
// The sentinel appears as the trailing tag in .Error() output; the
// human-actionable context comes first. Operators read it as
// `Error: BRANCH is required: usage error`.
var (
	// ErrUsage marks CLI invocation errors — missing required flags,
	// wrong positional arg count, bad flag values. Maps to ExitCodeUsage (2).
	ErrUsage = errors.New("usage error")

	// ErrInvalidConfig marks malformed config files (artifacts.yml
	// schema violations, parse failures). Maps to ExitCodeConfiguration (78).
	ErrInvalidConfig = errors.New("invalid configuration")

	// ErrMissingInput marks references to files / refs / artifacts
	// that don't exist on disk. Maps to ExitCodeNoInput (66).
	ErrMissingInput = errors.New("missing input")

	// ErrMalformedInput marks user-supplied content that exists but
	// fails to parse / decode (JSON, base64, encoded keys). Distinct
	// from ErrInvalidConfig (which is specifically about artifacts.yml
	// schema violations) and from ErrUsage (which is about CLI args).
	// Maps to ExitCodeDataErr (65).
	ErrMalformedInput = errors.New("malformed input")

	// ErrValidation marks domain rule failures — invalid tag format,
	// bad signature, changelog missing, namespace forbidden. The CI
	// step ran fine; the project's state failed a check. Maps to
	// ExitCodeValidation (1).
	ErrValidation = errors.New("validation failed")

	// ErrPermissionDenied marks auth/credential failures — missing
	// token, token rejected by the API, secret not set in env. Maps
	// to ExitCodeNoPerm (77).
	ErrPermissionDenied = errors.New("permission denied")

	// ErrDependencyUnavailable marks a dependency that could not be
	// reached: a registry, forge or download host that did not answer, or
	// answered that it is overloaded or down. It is the one class asking
	// again may pass, and the one a job is retried on, so nothing permanent
	// carries it. Maps to ExitCodeUnavailable (69).
	ErrDependencyUnavailable = errors.New("dependency unavailable")

	// ErrPrerequisite marks a job that lacks what the command needs to
	// run, or has it set up wrongly: a required binary (syft, trivy, git,
	// buildah) missing from PATH, a tool that could not complete for a
	// reason of its own setup, a runner without the expected package
	// manager. Asking again finds the same job, so it is not unavailable.
	// Maps to ExitCodeConfiguration (78).
	ErrPrerequisite = errors.New("prerequisite missing or misconfigured")

	// ErrUnsupported marks features the current platform doesn't
	// implement (multi-line outputs on GitLab, SLSA off-GitHub).
	// Provider/adapter implementations return this so callers can
	// branch on errors.Is(err, errs.ErrUnsupported) without depending
	// on the concrete adapter type. Maps to ExitCodeConfiguration (78).
	//
	// Deliberately NOT ExitCodeUnavailable (69): 69 is "external system
	// unavailable", which a caller is right to retry, and a capability the
	// platform does not have never becomes available by trying again. A
	// pipeline asking its forge for something that forge cannot do is
	// misconfigured for that forge, which is what 78 says.
	ErrUnsupported = errors.New("unsupported on this platform")

	// ErrReleaseNotFound is returned by release-provider adapters when
	// a queried release does not exist. Callers that *expect* absence
	// (cleanup-before-publish, "is this a re-run?") match with
	// errors.Is and treat it as a success path; other callers wrap and
	// propagate. Maps to ExitCodeNoInput (66) when it reaches main()
	// unhandled — the user asked for a release that isn't there.
	ErrReleaseNotFound = errors.New("release not found")

	// ErrRateLimited marks "the server told us 429 and our retries did
	// not clear it within the budget." Distinguished from
	// ErrDependencyUnavailable because the remedy is different (wait
	// vs investigate). Maps to ExitCodeUnavailable (69).
	ErrRateLimited = errors.New("rate limited")
)

// ExitCodeType classifies an error into the process exit code main()
// should return. Aligned with BSD sysexits.h for non-1/non-2 codes.
type ExitCodeType int

// Exit codes — POSIX 1/2 keep their conventional meanings; values >=64
// come from BSD sysexits.h.
const (
	ExitCodeOK            ExitCodeType = 0  // success
	ExitCodeValidation    ExitCodeType = 1  // rule failure (POSIX)
	ExitCodeUsage         ExitCodeType = 2  // CLI misuse / context-cancel
	ExitCodeDataErr       ExitCodeType = 65 // EX_DATAERR: malformed input
	ExitCodeNoInput       ExitCodeType = 66 // EX_NOINPUT: missing input file/ref
	ExitCodeUnavailable   ExitCodeType = 69 // EX_UNAVAILABLE: a dependency could not be reached
	ExitCodeSoftware      ExitCodeType = 70 // EX_SOFTWARE: internal bug
	ExitCodeNoPerm        ExitCodeType = 77 // EX_NOPERM: auth / permission
	ExitCodeConfiguration ExitCodeType = 78 // EX_CONFIG: bad config file
)

// FromHTTPStatus classifies an HTTP response status into one of the
// auth/dependency sentinels. Returns nil for status codes that don't
// have a clear mapping — callers either inline the catch-all
// (untyped → Software) or layer on a domain-specific sentinel.
//
// Used by adapter/github + adapter/gitlab + app/security/upload_sarif
// where the non-2xx path is otherwise the same wrap shape.
func FromHTTPStatus(status int) error {
	switch {
	case status == 401, status == 403:
		return ErrPermissionDenied
	case status == 404:
		return ErrMissingInput
	case status == 429:
		return ErrRateLimited
	case status == 408, status >= 500:
		// The engine's own client saw the status itself: the server was
		// there and could not serve. A child tool's text is held to less
		// (ToolFailure): of the statuses it prints only a 429 is asked again.
		return ErrDependencyUnavailable
	case status >= 400:
		// Every remaining 4xx — 409 Conflict, 400, 405, 422 — is the request
		// being refused, not the dependency being unavailable. Without this
		// case they returned nil and each adapter substituted
		// ErrDependencyUnavailable, so "GitLab says this release already
		// exists" exited 69 (unavailable) and read to a caller, and to a
		// retry loop, as "the forge is down". ExitCodeValidation is the
		// honest answer: the server was reachable and said no.
		return ErrValidation
	}

	return nil
}

// ExitCodeFromError maps an error to the process exit code main()
// should return. Each sentinel above corresponds to one case here;
// unrecognised non-nil errors fall through to ExitCodeSoftware — the
// "we got an error we didn't classify" bucket. The order of cases
// matters only insofar as a single err.Is might match multiple
// sentinels in pathological wraps; in practice each error matches
// one classification.
//
//nolint:cyclop // sysexits dispatch: one case per sentinel, refactoring would obscure the mapping.
func ExitCodeFromError(err error) ExitCodeType {
	switch {
	case err == nil:
		return ExitCodeOK
	case errors.Is(err, context.Canceled):
		return ExitCodeUsage
	case errors.Is(err, context.DeadlineExceeded):
		// A timeout — e.g. the adapters' 30s http.Client.Timeout firing
		// against a slow forge API, which surfaces as a
		// context.DeadlineExceeded-wrapping error — is a transient
		// external-dependency failure, not an internal bug. Without this
		// case it would fall through to ExitCodeSoftware (70), telling CI
		// to file a bug report for what is really "the registry was slow."
		return ExitCodeUnavailable
	case errors.Is(err, ErrUsage), errors.Is(err, ErrCIRuntimeRequired):
		return ExitCodeUsage
	case errors.Is(err, ErrValidation):
		return ExitCodeValidation
	case errors.Is(err, ErrMalformedInput):
		return ExitCodeDataErr
	case errors.Is(err, ErrMissingInput), errors.Is(err, ErrReleaseNotFound):
		return ExitCodeNoInput
	case errors.Is(err, ErrDependencyUnavailable), errors.Is(err, ErrRateLimited):
		return ExitCodeUnavailable
	case errors.Is(err, ErrPrerequisite):
		// Permanent like the next one: the job is set up wrongly for the
		// command, and a retry finds it unchanged.
		return ExitCodeConfiguration
	case errors.Is(err, ErrUnsupported):
		// Permanent, so it must not share a code with the transient two above:
		// the whole value of the distinction is that a caller can stop retrying.
		return ExitCodeConfiguration
	case errors.Is(err, ErrPermissionDenied):
		return ExitCodeNoPerm
	case errors.Is(err, ErrInvalidConfig):
		return ExitCodeConfiguration
	case isNetworkError(err):
		// A transport-level failure — connection refused, no route, DNS
		// failure — surfaces as a *net.OpError / *net.DNSError / *url.Error
		// with no domain sentinel. It is a transient external-dependency
		// problem ("the forge/registry was unreachable"), not an internal
		// defect, so it must not fall through to ExitCodeSoftware (70), which
		// tells CI to report a code defect. Checked after the sentinels so any explicit
		// sentinel (e.g. a 401 turned into ErrPermissionDenied) wins first.
		return ExitCodeUnavailable
	case errors.Is(err, fs.ErrNotExist):
		// A file or directory the command was pointed at is not there, and no
		// caller classified it: EX_NOINPUT, the same answer ErrMissingInput
		// gives, rather than "internal bug".
		return ExitCodeNoInput
	case errors.Is(err, fs.ErrPermission):
		return ExitCodeNoPerm
	default:
		return ExitCodeSoftware
	}
}

// IsTransport reports whether err is a request the network stopped: a name
// that did not resolve, a connection refused, reset or timed out. A
// certificate that does not verify, a TLS alert, a malformed URL and an
// operating-system error are not: the same request fails the same way.
//
// A body that ended before its length is a transfer the network cut.
func IsTransport(err error) bool {
	return isNetworkError(err) || errors.Is(err, io.ErrUnexpectedEOF)
}

// RequestFailure is the class of a request that ended in an error and no
// response: the dependency was unavailable when the network stopped it
// (IsTransport), and the request failed otherwise. Every adapter that makes
// its own requests classes such an error here, so only the first is ever
// asked again.
func RequestFailure(err error) error {
	if IsTransport(err) {
		return ErrDependencyUnavailable
	}

	return ErrValidation
}

// isNetworkError reports whether err is (or wraps) a transport-level network
// failure. net.Error is implemented by *net.OpError and *net.DNSError, and by
// *url.Error (which also unwraps to the underlying op error), so a single
// errors.As against the interface covers dial/DNS/refused failures from every
// HTTP-based adapter. Timeouts already match the context.DeadlineExceeded case
// above and land on the same code, so this is purely the non-timeout tail.
func isNetworkError(err error) bool {
	// A TLS alert arrives as a net.OpError too. The peer or this client
	// refused the handshake: a certificate, a protocol version. That is an
	// answer, and asking again gets the same one.
	if opErr, ok := errors.AsType[*net.OpError](err); ok && (opErr.Op == "remote error" || opErr.Op == "local error") {
		return false
	}

	// *url.Error implements net.Error, but it is also what url.Parse returns
	// for a malformed URL, before any request is built. That failure is the
	// caller's input and retrying it cannot succeed, so only a url.Error whose
	// cause is itself a network failure counts.
	if urlErr, ok := errors.AsType[*url.Error](err); ok {
		return isNetworkError(urlErr.Err)
	}

	var netErr net.Error
	if !errors.As(err, &netErr) {
		return false
	}

	// syscall.Errno has Timeout and Temporary methods, so it satisfies
	// net.Error too. A bare errno in the chain is an operating-system error
	// such as ENOENT from opening a file -- a socket failure arrives wrapped
	// in *net.OpError, which errors.As reaches first.
	_, isErrno := netErr.(syscall.Errno)

	return !isErrno
}
