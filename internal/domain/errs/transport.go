// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package errs

import (
	"errors"
	"regexp"
	"slices"
	"strings"
)

// nameResolution, connectFailure and readTimeout are the kinds several
// patterns give.
const (
	nameResolution = "name resolution"
	connectFailure = "connect failure"
	readTimeout    = "read timeout"
)

// transportEvidence is what a tool prints when the network, not the product,
// stopped it: a name that did not resolve, a connection that was refused,
// reset or unreachable, a connect or read that timed out. Each pattern is one
// tool's own wording and captures the host where the tool names it. It is
// the one list. Nothing else is evidence: not a finding, an HTTP status, a
// missing artifact, an exit status, a certificate the client refused or a
// "failed to fetch" without its reason.
//
// A name that does not exist reads as one that did not resolve: the resolver
// gives the same error for a typo as for a stall, so both count, and the
// attempts they cost are bounded where they are made.
//
// What the list decides is small, and must stay so: whether a failed child
// tool is run again a bounded number of times, and which class its failure
// exits with. It never decides a verdict. A tool's text can be made to look
// like a transport failure by what the tool read, so no pass, skip, warning
// or report-only result may rest on ToolFailure or on this list.
//
// The wordings were read from these versions: Go 1.27.3 (src/net/net.go,
// DNSError.Error and OpError.Error, "no such host" at :651; net/http's
// client timeout), curl 8.18.0 and Node 26.12.1 (run against a name that
// does not exist and a closed port), Trivy 0.76.1 (.mise.toml), mise
// 2026.10.8 and rustup 1.29.2 (their binaries' literals). glibc's
// "Temporary failure in name resolution" is gai_strerror(EAI_AGAIN) and its
// "Name or service not known" gai_strerror(EAI_NONAME), and urllib3's
// "Failed to resolve" its NameResolutionError; none was read from a pinned
// version. apt 3.2.1 (the literals of libapt-pkg and its http method),
// libcurl 8.18.0 and OpenSSH 10.2 (their binaries' literals), the Gradle
// wrapper 9.8.2 (org.gradle.wrapper.Download), Apache HttpClient 4.5.14 as
// Maven 3.10.0 ships it (HttpHostConnectException) and JDK 25 ("Connect
// timed out") were read on the developer's machine, not from the job
// images. An error string is no contract: a tool that rewords one is no
// longer retried, which fails safe.
var transportEvidence = []struct {
	kind string
	line *regexp.Regexp
}{
	// Go's resolver, as the engine, kaniko, Buildah, skopeo and cosign print it.
	{nameResolution, regexp.MustCompile(`lookup ([A-Za-z0-9.-]+)(?: on [0-9A-Fa-f.:\[\]]+)?: (?:read udp \S+: )?(?:i/o timeout|no such host|server misbehaving)`)},
	// Go's dialer and connections, from the same tools; the host is the address.
	{"connect timeout", regexp.MustCompile(`dial tcp \[?([0-9A-Fa-f.:]+?)\]?:\d+: i/o timeout`)},
	{"connection refused", regexp.MustCompile(`dial tcp \[?([0-9A-Fa-f.:]+?)\]?:\d+: connect: connection refused`)},
	{"network unreachable", regexp.MustCompile(`dial tcp \[?([0-9A-Fa-f.:]+?)\]?:\d+: connect: (?:network is unreachable|no route to host)`)},
	{"connection reset", regexp.MustCompile(`read tcp \S+->\[?([0-9A-Fa-f.:]+?)\]?:\d+: read: connection reset by peer`)},
	// Go's HTTP client, from the same tools.
	{readTimeout, regexp.MustCompile(`(?:Get|Head|Post|Put) \\?"https?://([^/"\\]+)[^"]*": (?:net/http: TLS handshake timeout|context deadline exceeded \(Client\.Timeout exceeded)`)},
	// curl, and git through libcurl. git gives libcurl's message without
	// curl's code; "Failed to connect to" is the message of a connect alone.
	{nameResolution, regexp.MustCompile(`Could not resolve host: ([A-Za-z0-9.-]+)`)},
	{connectFailure, regexp.MustCompile(`curl: \(7\) Failed to connect to ([A-Za-z0-9.:-]+) port \d+`)},
	{"connect timeout", regexp.MustCompile(`curl: \(28\) Failed to connect to ([A-Za-z0-9.:-]+) port \d+`)},
	{connectFailure, regexp.MustCompile(`fatal: unable to access '[^']*': Failed to connect to ([A-Za-z0-9.:-]+) port \d+`)},
	// ssh under git: a connect that failed, whatever the reason it gives.
	{connectFailure, regexp.MustCompile(`ssh: connect to host ([A-Za-z0-9.:-]+) port \d+: `)},
	// The C library's resolver, as Java (Maven, Gradle) and ssh print it.
	{nameResolution, regexp.MustCompile(`([A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+): (?:Temporary failure in name resolution|Name or service not known)`)},
	// apt's http method: its own wording of a name that did not resolve and
	// of a connect that failed or timed out.
	{nameResolution, regexp.MustCompile(`(?:Temporary failure resolving|Could not resolve) '([A-Za-z0-9.-]+)'`)},
	{connectFailure, regexp.MustCompile(`(?:Could not connect|Cannot initiate the connection) to ([A-Za-z0-9.-]+):\w+ \(`)},
	// Java's exceptions of a name and a connect, only behind the words a
	// stack trace puts before them: a class name alone is also a string a
	// tool can repeat from what it read, a certificate's identity for one.
	// The engine's own error line holds the trace's line after its action,
	// so the words are not held to the start of the line. An unknown host's
	// message begins with the name.
	{nameResolution, regexp.MustCompile(`(?:Exception in thread "main" |Caused by: )java\.net\.UnknownHostException: ([A-Za-z0-9.-]+)`)},
	{connectFailure, regexp.MustCompile(`(?:Exception in thread "main" |Caused by: )java\.net\.(?:ConnectException|NoRouteToHostException|SocketTimeoutException: Connect timed out|SocketException: Network is unreachable)`)},
	// The Gradle wrapper's own timeout of its download.
	{readTimeout, regexp.MustCompile(`Downloading from https?://([^/\s]+)\S* failed: timeout \(`)},
	// Apache HttpClient's connect failure, as Maven prints it.
	{connectFailure, regexp.MustCompile(`Connect to ([A-Za-z0-9.-]+):\d+ \[[^\]]*\] failed: `)},
	// Python's urllib3, as opengrep prints it when it fetches its rules.
	{nameResolution, regexp.MustCompile(`Failed to resolve '([A-Za-z0-9.-]+)'`)},
	// Node's resolver and sockets, as npm prints them.
	{nameResolution, regexp.MustCompile(`getaddrinfo (?:EAI_AGAIN|ENOTFOUND) ([A-Za-z0-9.-]+)`)},
	// mise's own timeout of a download: the literal "HTTP timed out after "
	// of the mise 2026.10.8 binary. It names no host on the line. reqwest's
	// "error sending request for url" is not listed: it also wraps a
	// certificate the client refused.
	{readTimeout, regexp.MustCompile(`^mise ERROR .*HTTP timed out after `)},
	// rustup's download of a toolchain file that timed out: "could not
	// download file from '<url>' to '<path>'" of the rustup 1.29.2 binary,
	// ending in reqwest's "operation timed out" (reqwest 0.13.5,
	// src/error.rs:426).
	{readTimeout, regexp.MustCompile(`error: could not download file from 'https?://([^/']+)[^']*' to '[^']*'.*: operation timed out`)},
}

// answerEvidence is what a tool prints when the other side answered or the
// tool itself refused what it received: an HTTP status, a registry's error
// code, a checksum or a signature that does not match. An answer is not
// changed by asking again.
var answerEvidence = []*regexp.Regexp{
	// A status as Go's registry client, reqwest, curl and git, and the Go and
	// Cargo downloads print one.
	regexp.MustCompile(`unexpected status code [1-5]\d\d\b`),
	regexp.MustCompile(`HTTP status (?:client|server) error \([45]\d\d\b`),
	regexp.MustCompile(`The requested URL returned error: [45]\d\d\b`),
	regexp.MustCompile(`\b[45]\d\d (?:Bad Request|Unauthorized|Forbidden|Not Found|Gone|Too Many Requests|Internal Server Error|Bad Gateway|Service Unavailable|Gateway Timeout)\b`),
	regexp.MustCompile(`\bgot [45]\d\d\b`),
	regexp.MustCompile(`(?i)\b(?:http|status(?: code)?:?|error:?)\s+[45]\d\d\b`),
	// A certificate the client refused, in Go's, OpenSSL's, curl's and
	// Node's wording.
	regexp.MustCompile(`x509: |tls: |(?i)\bcertificate (?:verify failed|signed by unknown authority|has expired|is not trusted)|SSL certificate problem|SELF_SIGNED_CERT|UNABLE_TO_(?:GET_ISSUER_CERT|VERIFY_LEAF_SIGNATURE)|CERT_HAS_EXPIRED`),
	// A registry's error code.
	regexp.MustCompile(`\b(?:DENIED|UNAUTHORIZED|MANIFEST_UNKNOWN|NAME_UNKNOWN|BLOB_UNKNOWN|TOOMANYREQUESTS)\b`),
	// npm's code of a status.
	regexp.MustCompile(`^npm (?:error|ERR!) code E[45]\d\d\b`),
	// What the tool refused itself: in Maven's, Java's and apt's wording too.
	regexp.MustCompile(`(?i)\bchecksum (?:mismatch|failed|validation failed)\b`),
	regexp.MustCompile(`PKIX path|Hash Sum mismatch`),
	regexp.MustCompile(`SECURITY ERROR`),
	regexp.MustCompile(`(?i)\b(?:no matching signatures|invalid signature|signature mismatch)\b`),
}

// rateLimited is the one answer worth another attempt: a 429, in the same
// wordings, and the registry code for it.
var rateLimited = regexp.MustCompile(`\bTOOMANYREQUESTS\b|(?i)\b(?:code|error \(|error:?|got|http|status)\s*429\b|\b429 Too Many Requests\b|code E429\b`)

// TransportEvidence reads one line of a tool's error report and gives the
// transport failure it states: its kind, the host when the tool names one,
// and whether it states one.
//
// A builder's warning that it retries is no evidence: the attempt after it
// may succeed, and the one that ends the tool says so in a line of its own.
func TransportEvidence(line string) (string, string, bool) {
	if strings.Contains(line, "retrying in ") {
		return "", "", false
	}

	for _, evidence := range transportEvidence {
		if match := evidence.line.FindStringSubmatch(line); match != nil {
			if len(match) > 1 {
				return evidence.kind, match[1], true
			}

			return evidence.kind, "", true
		}
	}

	return "", "", false
}

// ToolFailure is the class of a child tool's failure, read from the tool's
// own error report. An answer decides: a status, a registry's error code, a
// checksum or signature the tool refused is the tool's failure, whatever
// else the report says, and a 429 alone is a rate limit. A transport failure
// counts only when nothing answered, and is the dependency being
// unavailable. Only that and the rate limit are worth another attempt.
//
// The report is the tool's own error lines and nothing it repeats from what
// it read: the caller selects them (TrivyReport, MiseReport, GoReport,
// CargoReport, CosignReport, BuildahReport, KanikoReport, AptReport,
// GradleWrapperReport, MavenReport, GitReport). The class selects a bounded
// retry and an exit code and nothing else: see transportEvidence.
func ToolFailure(report string) error {
	var answered, limited, transport bool

	for line := range strings.SplitSeq(report, "\n") {
		switch {
		case answers(line):
			if rateLimited.MatchString(line) {
				limited = true
			} else {
				answered = true
			}
		case !transport:
			_, _, transport = TransportEvidence(line)
		}
	}

	switch {
	case answered:
		return ErrValidation
	case limited:
		return ErrRateLimited
	case transport:
		return ErrDependencyUnavailable
	default:
		return ErrValidation
	}
}

func answers(line string) bool {
	for _, answer := range answerEvidence {
		if answer.MatchString(line) {
			return true
		}
	}

	return false
}

// Retriable reports whether a ToolFailure class is worth another attempt.
func Retriable(class error) bool {
	return errors.Is(class, ErrDependencyUnavailable) || errors.Is(class, ErrRateLimited)
}

// scannedField is a field of a log line that repeats what the tool read:
// a path, a name or a message from the scanned content.
var scannedField = regexp.MustCompile(`\b[A-Za-z_]+="(?:[^"\\]|\\.)*"`)

// trivyFatal is the line Trivy ends on: its level, after the timestamp.
var trivyFatal = regexp.MustCompile(`^(?:\S+\s+)?FATAL\b`)

// TrivyReport is Trivy's own error report in its output: the FATAL line it
// ends on and the lines of that error after it, without the fields that
// repeat scanned content. Its other lines describe what it scanned.
func TrivyReport(output string) string {
	lines := strings.Split(output, "\n")

	for at, line := range slices.Backward(lines) {
		if trivyFatal.MatchString(line) {
			return scannedField.ReplaceAllString(strings.Join(lines[at:], "\n"), "")
		}
	}

	return ""
}

// MiseReport is mise's own error report in its output: its "mise ERROR"
// lines, and the one line rustup ends a failed download on when mise runs
// it. A hook's and a tool's other output is not part of it.
func MiseReport(output string) string {
	return ownLines(output, "mise ERROR ", "error: could not download file from '")
}

// GoReport is the go command's own error report: its "go: " lines.
func GoReport(output string) string { return ownLines(output, "go: ") }

// CargoReport is cargo's own error report: its "error" line and the causes
// it lists below it.
func CargoReport(output string) string { return fromLine(output, "error") }

// CosignReport is cosign's own error report: its "Error: " line and what
// follows it.
func CosignReport(output string) string { return fromLine(output, "Error: ") }

// buildahStep is the Containerfile instruction Buildah quotes in the error
// of a failed step: "building at STEP \"%s\": %w" (buildah 1.42.0,
// imagebuildah/stage_executor.go:1450). Buildah escapes nothing in it, so it
// ends at the last `": ` of the report and not at the first.
var buildahStep = regexp.MustCompile(`(?s)^Error: building at STEP ".*": `)

// BuildahReport is Buildah's own error report in a build's output: the
// "Error: " line it ends on and what follows it, without the instruction a
// failed step's error quotes. Everything before that line is the build: a
// RUN step's output, which is the Containerfile's own, and the warnings of
// pulls Buildah tried again. The instruction is the Containerfile's too.
//
// Cutting to the last `": ` also cuts a `Get "<url>": ` of the step's cause,
// so a step that timed out reading a registry stays the build's failure; a
// name that did not resolve and a dial that failed are stated after it.
func BuildahReport(output string) string {
	return buildahStep.ReplaceAllLiteralString(fromLastLine(output, "Error: "), "Error: ")
}

// KanikoReport is kaniko's own error report in a build's output: the
// "error building image: " line it ends on. Everything before that line is
// the build, a RUN step's output included, and kaniko quotes no instruction
// in the error of a failed step.
func KanikoReport(output string) string {
	return fromLastLine(output, "error building image: ")
}

// aptFetch is apt-get's line for a file it could not fetch, and captures
// the reason after the URL.
var aptFetch = regexp.MustCompile(`^[WE]: Failed to fetch \S+  (.+)$`)

// aptUnread is an error of apt-get that a package source it could not read
// explains: the package is not in an index that was never fetched.
var aptUnread = regexp.MustCompile(`^E: (?:Unable to locate package |Unable to fetch some archives|Failed to fetch some archives|Some index files failed to download)`)

// AptReport is apt-get's own account of the files it could not fetch, in
// the output of an update and the install after it: the reason of each
// "Failed to fetch" line. An update that cannot read a source only warns
// and exits 0, and the install then ends on "Unable to locate package", so
// the update's warnings are part of the report.
//
// It is empty unless every file apt-get could not fetch failed on a host
// that was not reached: a hash that does not match, a signature apt-get
// refuses and a status are the archive's, and one of them beside an
// unreached host is enough. It is empty too when apt-get gives any other
// error, a package script that failed or a dependency that cannot be met,
// since a source that was not read does not explain those.
//
// What it cannot tell apart: a package whose name is wrong, asked for after
// an update that could not read a source, ends on the same "Unable to
// locate package" as one the unread source would have held.
func AptReport(output string) string {
	var reasons []string

	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)

		if fetch := aptFetch.FindStringSubmatch(line); fetch != nil {
			if _, _, unreached := TransportEvidence(fetch[1]); !unreached {
				return ""
			}

			reasons = append(reasons, fetch[1])
		} else if strings.HasPrefix(line, "E: ") && !aptUnread.MatchString(line) {
			return ""
		}
	}

	return strings.Join(reasons, "\n")
}

// GradleWrapperReport is the Gradle wrapper's own error report: the
// exception that ended it while it downloaded the Gradle distribution, and
// that exception's causes. It is empty unless the stack trace passes through
// the wrapper's download, so an exception a build, a test or a daemon prints
// is not part of it; what Gradle reports of a failed dependency resolution
// is not read at all.
func GradleWrapperReport(output string) string {
	trace := fromLine(output, `Exception in thread "main" `)
	if !strings.Contains(trace, "\tat org.gradle.wrapper.Download.") {
		return ""
	}

	return ownLines(trace, `Exception in thread "main" `, "Caused by: ")
}

// mavenTransfer begins Maven's account of a file it could not transfer.
const mavenTransfer = "Could not transfer "

// mavenEnding is the end of a transfer Maven could not make: the repository
// as "from/to <id> (<url>)", the host of its URL and the reason Maven gives.
var mavenEnding = regexp.MustCompile(`(from/to \S+ \(https?://([^/:)]+)[^)]*\)): (.*)$`)

// MavenReport is Maven's own account of the transfers that ended a build:
// the "[ERROR]" lines after "BUILD FAILURE" that state one, and the
// "[WARNING]" lines before it about the same repository. A build that ends
// on failed tests or a compile error has none, whatever was printed before.
//
// Maven's last error often repeats a transfer with the host's name for its
// only reason, and the warning of the same repository has the reason, so
// that warning is part of the report. A warning about another repository is
// not: a repository that is gone does not explain a build another one
// ended. The report is empty when a transfer that ended the build states a
// reason of its own that is no transport failure: a checksum, a
// certificate, a status, a blocked mirror. It is empty too when a line
// names more than one repository.
func MavenReport(output string) string {
	at := strings.LastIndex(output, "] BUILD FAILURE")
	if at < 0 {
		return ""
	}

	var report, repositories []string

	for line := range strings.SplitSeq(ownLines(output[at:], "[ERROR] "), "\n") {
		if !strings.Contains(line, mavenTransfer) {
			continue
		}

		ending := mavenEnding.FindStringSubmatch(line)
		if ending == nil || strings.Count(line, "from/to ") != 1 {
			return ""
		}

		reason := strings.TrimSuffix(ending[3], " -> [Help 1]")
		if _, _, unreached := TransportEvidence(reason); reason != ending[2] && !unreached {
			return ""
		}

		report, repositories = append(report, line), append(repositories, ending[1])
	}

	for line := range strings.SplitSeq(ownLines(output[:at], "[WARNING] "), "\n") {
		if strings.Contains(line, mavenTransfer) && slices.ContainsFunc(repositories, func(repository string) bool { return strings.Contains(line, repository) }) {
			report = append(report, line)
		}
	}

	return strings.Join(report, "\n")
}

// GitReport is git's own error report of a remote it could not reach: its
// "fatal: unable to access" line over HTTP and ssh's lines. What the remote
// sent, which git prefixes with "remote: ", is not part of it.
func GitReport(output string) string {
	return ownLines(output, "fatal: unable to access '", "ssh: ")
}

// ownLines keeps the lines that begin with one of a tool's own prefixes.
func ownLines(output string, prefixes ...string) string {
	var own []string

	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range prefixes {
			if strings.HasPrefix(line, prefix) {
				own = append(own, line)

				break
			}
		}
	}

	return strings.Join(own, "\n")
}

// fromLastLine keeps the output from the last line with the prefix on.
func fromLastLine(output, prefix string) string {
	lines := strings.Split(output, "\n")

	for at, line := range slices.Backward(lines) {
		if strings.HasPrefix(line, prefix) {
			return strings.Join(lines[at:], "\n")
		}
	}

	return ""
}

// fromLine keeps the output from the first line with the prefix on.
func fromLine(output, prefix string) string {
	if strings.HasPrefix(output, prefix) {
		return output
	}

	if at := strings.Index(output, "\n"+prefix); at >= 0 {
		return output[at+1:]
	}

	return ""
}

// npmCode is the line npm names its error on.
var npmCode = regexp.MustCompile(`^npm (?:error|ERR!) code (\S+)$`)

// NPMFailure is the class of a failed npm command, read from the error
// code npm prints on a line of its own. A registry that could not be
// resolved, reached or kept is unavailable, and E429 is a rate limit.
// Every other code is npm's answer or the registry's: a status (E401,
// E403, E404), a certificate it refused, a usage error.
func NPMFailure(stderr string) error {
	class := ErrValidation

	for line := range strings.SplitSeq(stderr, "\n") {
		match := npmCode.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}

		switch match[1] {
		case "ENOTFOUND", "EAI_AGAIN", "ECONNREFUSED", "ECONNRESET", "ETIMEDOUT", "ENETUNREACH", "EHOSTUNREACH",
			"ECONNECTIONTIMEOUT", "EIDLETIMEOUT", "ERESPONSETIMEOUT", "ETRANSFERTIMEOUT":
			class = ErrDependencyUnavailable
		case "E429":
			class = ErrRateLimited
		default:
			return ErrValidation
		}
	}

	return class
}
