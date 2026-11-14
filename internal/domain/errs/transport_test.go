// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package errs_test

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/url"
	"os"
	"syscall"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// The lines with a timestamp are the ones the lab's runs printed on
// 2026-11-11 while the site's resolver did not answer, as the retained logs
// hold them; the other positive lines are the same and other tools' wording
// of the remaining transport failures. The rest look alike and are the
// product's, or nobody's, to explain.
func TestTransportEvidenceIsOnlyATransportFailureWithItsHost(t *testing.T) {
	t.Parallel()

	for line, want := range map[string]string{
		// The engine's tool download.
		`        2026-11-11T12:23:38.9347345Z Error: download https://github.com/orhun/git-cliff/releases/download/v2.14.2/git-cliff-2.14.2-x86_64-unknown-linux-musl.tar.gz: Get "https://github.com/orhun/git-cliff/releases/download/v2.14.2/git-cliff-2.14.2-x86_64-unknown-linux-musl.tar.gz": dial tcp: lookup github.com on 10.201.0.1:53: read udp 10.201.13.222:52721->10.201.0.1:53: i/o timeout: dependency unavailable`: "name resolution (github.com)",
		`        2026-11-11T07:02:26.9853535Z Error: download https://github.com/orhun/git-cliff/releases/download/v2.14.2/git-cliff-2.14.2-x86_64-unknown-linux-musl.tar.gz: Get "https://github.com/orhun/git-cliff/releases/download/v2.14.2/git-cliff-2.14.2-x86_64-unknown-linux-musl.tar.gz": dial tcp: lookup github.com: i/o timeout: dependency unavailable`:                                                               "name resolution (github.com)",
		// kaniko's base image.
		`        2026-11-11T09:56:02.1363330Z error building image: unable to complete operation after 0 attempts, last error: Get "https://index.docker.io/v2/": dial tcp: lookup index.docker.io: i/o timeout`: "name resolution (index.docker.io)",
		// Buildah's base image: the error of its logged retries, as the line
		// that ends the build gives it.
		`        Error: creating build container: initializing source docker://gcr.io/distroless/static: pinging container registry gcr.io: Get "https://gcr.io/v2/": dial tcp: lookup gcr.io on 10.201.0.1:53: read udp 10.201.5.198:39907->10.201.0.1:53: i/o timeout`: "name resolution (gcr.io)",
		// Maven's repository, and opengrep's rules.
		`        2026-11-11T11:10:59.719579Z 01O [WARNING] Could not transfer metadata /.meta/prefixes.txt from/to central (https://repo.maven.apache.org/maven2): repo.maven.apache.org: Temporary failure in name resolution`:                                                                                    "name resolution (repo.maven.apache.org)",
		`        2026-11-11T11:19:46.272068Z 01E HTTPSConnectionPool(host='semgrep.dev', port=443): Max retries exceeded with url: /p/default (Caused by NameResolutionError("HTTPSConnection(host='semgrep.dev', port=443): Failed to resolve 'semgrep.dev' ([Errno -3] Temporary failure in name resolution)"))`: "name resolution (semgrep.dev)",
		// git and curl.
		`        fatal: unable to access 'https://github.com/org/repo/': Could not resolve host: github.com`: "name resolution (github.com)",
		`        curl: (7) Failed to connect to github.com port 443 after 3 ms: Couldn't connect to server`:  "connect failure (github.com)",
		// The dialer, the connection and the HTTP client of a Go tool.
		`        Get "https://registry.example/v2/": dial tcp 192.0.2.7:443: connect: connection refused`:                                           "connection refused (192.0.2.7)",
		`        Get "https://registry.example/v2/": dial tcp 192.0.2.7:443: i/o timeout`:                                                           "connect timeout (192.0.2.7)",
		`        Get "https://registry.example/v2/": dial tcp 192.0.2.7:443: connect: network is unreachable`:                                       "network unreachable (192.0.2.7)",
		`        read tcp 10.0.0.2:41234->192.0.2.7:443: read: connection reset by peer`:                                                            "connection reset (192.0.2.7)",
		`        Get "https://registry.example/v2/app/blobs/sha256:ab": net/http: TLS handshake timeout`:                                            "read timeout (registry.example)",
		`        Get "https://registry.example/v2/": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`:                    "read timeout (registry.example)",
		`        npm error errno EAI_AGAIN request to https://registry.npmjs.org/left-pad failed, reason: getaddrinfo EAI_AGAIN registry.npmjs.org`: "name resolution (registry.npmjs.org)",
		// A timeout as mise and rustup word it; mise names no host.
		`mise ERROR Failed to install core:go@1.26.6: HTTP timed out after 30s`: "read timeout ()",
		`error: could not download file from 'https://static.example/dist/channel-rust-1.90.0.toml' to '/tmp/x': error sending request for url (https://static.example/dist/channel-rust-1.90.0.toml): operation timed out`: "read timeout (static.example)",

		// A builder that retries has not failed yet.
		`        2026-11-11T09:19:42.228797Z 01E time="2026-11-11T09:19:42Z" level=warning msg="Failed, retrying in 1s ... (1/3). Error: initializing source docker://app: pinging container registry registry-1.docker.io: Get \"https://registry-1.docker.io/v2/\": dial tcp: lookup registry-1.docker.io on 10.201.0.1:53: read udp 10.201.5.198:34477->10.201.0.1:53: i/o timeout"`: "",
		// Maven's summary of a failed resolution gives no reason.
		`        2026-11-11T07:42:38.543735Z 01O [ERROR] 	The following artifacts could not be resolved: org.apache.maven.plugins:maven-resources-plugin:pom:3.5.0 (absent): Could not transfer artifact org.apache.maven.plugins:maven-resources-plugin:pom:3.5.0 from/to central (https://repo.maven.apache.org/maven2): repo.maven.apache.org`: "",
		`        2026-11-11T12:54:56.6241185Z [ERROR] [Help 1] http://cwiki.apache.org/confluence/display/MAVEN/PluginResolutionException`: "",
		// A Java class name outside a stack trace is what a tool read: here
		// the identities of a certificate, as cosign repeats them.
		`Error: none of the expected identities matched what was in the certificate, got subjects [java.net.ConnectException@evil.example] with issuer https://issuer.example`:                                                           "",
		`Error: none of the expected identities matched what was in the certificate, got subjects [https://forge.example/java.net.ConnectException/repo/.forgejo/workflows/release.yml@refs/tags/v1] with issuer https://issuer.example`: "",
		`Error: ./gradlew test: exit status 1: Exception in thread "main" java.net.UnknownHostException: services.gradle.org: dependency unavailable`:                                                                                    "name resolution (services.gradle.org)",
		`Exception in thread "main" java.net.UnknownHostException: services.gradle.org`:                                                                                                                                                  "name resolution (services.gradle.org)",
		`Caused by: java.net.ConnectException: Connection refused`:                                                                                                                                                                       "connect failure ()",
		// Statuses, missing things, findings and refusals.
		`        Error: staged linux/amd64: trivy image exited with status 1: dependency unavailable`:                             "",
		`        Error: artifact build-dist not found`:                                                                            "",
		`        Error: GET https://registry.example/v2/app/manifests/v1: unexpected status code 404 Not Found`:                   "",
		`        Error: GET https://registry.example/v2/app/manifests/v1: unexpected status code 401 Unauthorized`:                "",
		`        Error: GET https://registry.example/v2/: unexpected status code 503 Service Unavailable`:                         "",
		`        error: failed to fetch https://example.org/tool.tar.gz`:                                                          "",
		`        ERROR: Job failed: exit code 1`:                                                                                  "",
		`        Error: gitlaboutput: multiline output "layouts" is unsupported; use ManifestSink: unsupported on this platform`:  "",
		`        --- FAIL: TestClientTimesOut (0.01s): the request did not time out: want a timeout, got a response after 5s`:     "",
		`        POST https://forgejo.example/api/v1/repos/o/r/actions/runs/1075/rerun-failed-jobs: HTTP 404: 404 page not found`: "",
		`        Error: checksum mismatch for git-cliff-2.14.2-x86_64-unknown-linux-musl.tar.gz`:                                  "",
		`        context deadline exceeded`:                                                                                       "",
		`        mise ERROR HTTP status client error (404 Not Found) for url (https://mise-versions.jdx.dev/tools/node.toml)`:     "",
		// reqwest's envelope alone also wraps a certificate the client refused.
		`mise ERROR error sending request for url (https://mise-versions.jdx.dev/tools/node.toml)`:                                     "",
		`        Get "https://registry.example/v2/": tls: failed to verify certificate: x509: certificate signed by unknown authority`: "",
	} {
		got := ""
		if kind, host, found := errs.TransportEvidence(line); found {
			got = kind + " (" + host + ")"
		}

		if got != want {
			t.Errorf("TransportEvidence(%q) = %q, want %q", line, got, want)
		}
	}
}

// A tool's failure is the dependency's only when its report states a
// transport failure and no answer: a mirror that refused the connection
// does not make the 404, the registry's refusal or the checksum mismatch
// after it worth asking again. A 429 is the one answer that is.
func TestToolFailureLetsAnAnswerOutweighAnUnreachedHost(t *testing.T) {
	t.Parallel()

	const refused = "FATAL init error: 2 errors occurred:\n\t* Get \"https://mirror.example/v2/\": dial tcp 192.0.2.7:443: connect: connection refused\n"

	for output, want := range map[string]error{
		"FATAL init error: Get \"https://mirror.example/v2/\": dial tcp: lookup mirror.example: no such host\n": errs.ErrDependencyUnavailable,
		refused: errs.ErrDependencyUnavailable,
		refused + "\t* GET https://registry.example/v2/db/manifests/2: unexpected status code 404 Not Found\n":                                                                                                                errs.ErrValidation,
		refused + "\t* GET https://registry.example/v2/db/manifests/2: MANIFEST_UNKNOWN: manifest unknown\n":                                                                                                                  errs.ErrValidation,
		refused + "\t* GET https://registry.example/v2/db/manifests/2: DENIED: requested access is denied\n":                                                                                                                  errs.ErrValidation,
		refused + "\t* Get \"https://other.example/v2/\": tls: failed to verify certificate: x509: certificate signed by unknown authority\n":                                                                                 errs.ErrValidation,
		refused + "\t* GET https://registry.example/v2/db/manifests/2: HTTP 404\n":                                                                                                                                            errs.ErrValidation,
		refused + "\t* fetch failed with status code 403\n":                                                                                                                                                                   errs.ErrValidation,
		refused + "\t* curl: (60) SSL certificate problem: unable to get local issuer certificate\n":                                                                                                                          errs.ErrValidation,
		refused + "\t* request failed: HTTP 429\n":                                                                                                                                                                            errs.ErrRateLimited,
		refused + "\t* checksum mismatch for db.tar.gz\n":                                                                                                                                                                     errs.ErrValidation,
		refused + "\t* GET https://registry.example/v2/db/manifests/2: TOOMANYREQUESTS: retry-after: 60s\n":                                                                                                                   errs.ErrRateLimited,
		"FATAL GET https://registry.example/v2/db/manifests/2: TOOMANYREQUESTS: retry-after: 60s\n":                                                                                                                           errs.ErrRateLimited,
		"GET https://registry.example/v2/app/manifests/v1: unexpected status code 429 Too Many Requests\n":                                                                                                                    errs.ErrRateLimited,
		"\t* TOOMANYREQUESTS: slow down\n\t* MANIFEST_UNKNOWN: manifest unknown\n":                                                                                                                                            errs.ErrValidation,
		"GET https://registry.example/v2/: unexpected status code 503 Service Unavailable\n":                                                                                                                                  errs.ErrValidation,
		"FATAL image scan error: unable to find the specified image\n":                                                                                                                                                        errs.ErrValidation,
		"verifying module: checksum mismatch\n\tdownloaded: h1:aa\n\tgo.sum:     h1:bb\nSECURITY ERROR\n":                                                                                                                     errs.ErrValidation,
		"go: example.org/m@v1.0.0: Get \"https://proxy.example/m/@v/v1.0.0.zip\": dial tcp: lookup proxy.example: no such host\nverifying example.org/n@v1.0.0: checksum mismatch\n":                                          errs.ErrValidation,
		"Get \"https://registry.example/v2/\": tls: failed to verify certificate: x509: certificate signed by unknown authority\n":                                                                                            errs.ErrValidation,
		"mise ERROR Failed to install core:go@1.26.6: HTTP timed out after 30s\n":                                                                                                                                             errs.ErrDependencyUnavailable,
		"mise ERROR error sending request for url (https://mise-versions.jdx.dev/tools/node.toml)\n":                                                                                                                          errs.ErrValidation,
		"mise ERROR Failed to install core:go@1.26.6: Checksum mismatch\n":                                                                                                                                                    errs.ErrValidation,
		"error: could not download file from 'https://static.example/dist/channel-rust-1.90.0.toml' to '/tmp/x': error sending request for url (https://static.example/dist/channel-rust-1.90.0.toml): operation timed out\n": errs.ErrDependencyUnavailable,
		"error: could not download file from 'https://static.example/dist/channel-rust-1.90.0.toml' to '/tmp/x': checksum failed\n":                                                                                           errs.ErrValidation,
		"npm error code E404\nnpm error 404 Not Found - GET https://registry.example/pkg\n":                                                                                                                                   errs.ErrValidation,
		"": errs.ErrValidation,
	} {
		if got := errs.ToolFailure(output); !errors.Is(got, want) {
			t.Errorf("ToolFailure(%q) = %v, want %v", output, got, want)
		}
	}
}

// Only a tool's own error lines are its report. What it repeats from the
// content it read, and what a hook or a program it ran printed, can say
// anything.
func TestAToolsReportLeavesOutWhatItRead(t *testing.T) {
	t.Parallel()

	const lookalike = "Could not resolve host: demo.example"

	for name, report := range map[string]string{
		"a scanned path in a Trivy warning":   errs.TrivyReport("2026-11-11T12:00:00Z\tWARN\tskipped file_path=\"/app/" + lookalike + "\"\n2026-11-11T12:00:01Z\tFATAL\tFatal error\timage scan error: unable to find the specified image\n"),
		"a scanned path on Trivy's last line": errs.TrivyReport("2026-11-11T12:00:01Z\tFATAL\tFatal error\tanalyze error file_path=\"/app/" + lookalike + "\"\n"),
		"Trivy output without a fatal line":   errs.TrivyReport("INFO " + lookalike + "\n"),
		"a tool mise ran":                     errs.MiseReport("error: " + lookalike + "\nmise ERROR tool failed\n"),
		"a module's own output under go":      errs.GoReport("# example.org/m\n" + lookalike + "\ngo: example.org/m@v1.0.0: invalid version\n"),
		"a build script's output under cargo": errs.CargoReport("warning: " + lookalike + "\nerror: failed to parse manifest\n"),
		"what cosign prints before its error": errs.CosignReport(lookalike + "\nError: no matching signatures\n"),
		"a mise hook's output":                errs.MiseReport("exit status 1\n" + lookalike + "\ndial tcp 127.0.0.1:9: connect: connection refused\nmise ERROR hook failed\n"),
	} {
		if got := errs.ToolFailure(report); !errors.Is(got, errs.ErrValidation) {
			t.Errorf("%s: ToolFailure(%q) = %v, want the tool's own failure", name, report, got)
		}
	}

	for name, report := range map[string]string{
		"Trivy's fatal error and its list": errs.TrivyReport("2026-11-11T12:00:00Z\tINFO\tdownloading\n2026-11-11T12:00:01Z\tFATAL\tFatal error\tinit error: 1 error occurred:\n\t* Get \"https://mirror.example/v2/\": dial tcp: lookup mirror.example: no such host\n"),
		"go's own error":                   errs.GoReport("go: downloading example.org/m v1.0.0\ngo: example.org/m@v1.0.0: Get \"https://proxy.example/m/@v/v1.0.0.zip\": dial tcp: lookup proxy.example: no such host\n"),
		"cargo's own error":                errs.CargoReport("    Updating crates.io index\nerror: failed to get `m` as a dependency\n\nCaused by:\n  [6] Could not resolve hostname (Could not resolve host: index.crates.io)\n"),
		"cosign's own error":               errs.CosignReport("Error: Get \"https://registry.example/v2/\": dial tcp: lookup registry.example: no such host\nmain.go:74: error during command execution\n"),
		"mise's own error":                 errs.MiseReport("exit status 1\nmise ERROR Failed to install core:go@1.26.6: HTTP timed out after 30s\nmise ERROR Run with --verbose or MISE_VERBOSE=1 for more information\n"),
	} {
		if got := errs.ToolFailure(report); !errors.Is(got, errs.ErrDependencyUnavailable) {
			t.Errorf("%s: ToolFailure(%q) = %v, want unavailable", name, report, got)
		}
	}
}

// Buildah's report is the error it ends on. A registry it could not reach
// is unavailable; a RUN step that failed, a registry that answered and a
// pull it tried again before another error are the build's own failure.
func TestBuildahReportIsTheErrorItEndsOn(t *testing.T) {
	t.Parallel()

	const (
		retrying  = `time="2026-11-11T22:48:40Z" level=warning msg="Failed, retrying in 15s ... (1/5). Error: initializing source docker://registry.example/base:1: pinging container registry registry-1.docker.io: Get \"https://registry-1.docker.io/v2/\": dial tcp: lookup registry-1.docker.io: i/o timeout"` + "\n"
		unreached = `Error: creating build container: initializing source docker://gcr.io/distroless/cc-debian13@sha256:e792ab3d241a468a4fd7519ddbbebe66b49b5f365771716ea688ad40b6c6f1c2: pinging container registry gcr.io: Get "https://gcr.io/v2/": dial tcp: lookup gcr.io on 10.201.0.1:53: server misbehaving` + "\n"
		source    = "Error: creating build container: initializing source docker://registry.example/base:1: "
	)

	for name, scenario := range map[string]struct {
		output string
		want   error
	}{
		"a registry that was not reached":       {"[1/2] STEP 1/2: FROM gcr.io/distroless/cc-debian13\n" + unreached, errs.ErrDependencyUnavailable},
		"not reached after retries":             {retrying + unreached, errs.ErrDependencyUnavailable},
		"a RUN step that reached no host":       {"[1/2] STEP 2/2: RUN ./fetch\nError: dial tcp: lookup mirror.example: i/o timeout\nError: building at STEP \"RUN ./fetch\": while running runtime: exit status 1\n", errs.ErrValidation},
		"a RUN step whose text names no host":   {"Error: building at STEP \"RUN echo \"fetch\": lookup mirror.example: no such host && false\": while running runtime: exit status 1\n", errs.ErrValidation},
		"a RUN step of several lines":           {"Error: building at STEP \"RUN <<EOF\necho lookup mirror.example: no such host\nEOF\": while running runtime: exit status 1\n", errs.ErrValidation},
		"a step that did not reach a registry":  {"Error: building at STEP \"COPY --from=registry.example/tools:1 /bin/tool /bin/tool\": initializing source docker://registry.example/tools:1: pinging container registry registry.example: Get \"https://registry.example/v2/\": dial tcp: lookup registry.example: no such host\n", errs.ErrDependencyUnavailable},
		"a registry that refused the login":     {source + "reading manifest 1 in registry.example/base: unauthorized: authentication required\n", errs.ErrValidation},
		"a registry that answered 401":          {source + `pinging container registry registry.example: received unexpected HTTP status: 401 Unauthorized` + "\n", errs.ErrValidation},
		"a registry that denied the pull":       {source + "reading manifest 1 in registry.example/base: denied: requested access to the resource is denied\n", errs.ErrValidation},
		"a manifest the registry does not have": {source + "reading manifest 1 in registry.example/base: manifest unknown\n", errs.ErrValidation},
		"a retried pull, then another error":    {retrying + source + "reading manifest 1 in registry.example/base: manifest unknown\n", errs.ErrValidation},
		"a build that printed no error":         {retrying, errs.ErrValidation},
	} {
		if got := errs.ToolFailure(errs.BuildahReport(scenario.output)); !errors.Is(got, scenario.want) {
			t.Errorf("%s: ToolFailure(BuildahReport(%q)) = %v, want %v", name, scenario.output, got, scenario.want)
		}
	}
}

// The reports of the tools a job fetches with. apt-get's "Temporary failure
// resolving" and "Unable to locate package" are from a job log of
// 2026-11-12, when the job's resolver did not answer, and Maven's and
// kaniko's lines from logs of the day before; the other lines follow the
// formats the tools' binaries hold. What only looks like an unreached host,
// in a test's or a step's output or beside another error, is the project's.
func TestAFetchingToolsReportIsItsOwnTransferFailure(t *testing.T) {
	t.Parallel()

	const (
		aptUnresolved = "Err:1 http://deb.debian.org/debian trixie InRelease\n  Temporary failure resolving 'deb.debian.org'\nReading package lists...\n" +
			"W: Failed to fetch http://deb.debian.org/debian/dists/trixie/InRelease  Temporary failure resolving 'deb.debian.org'\n" +
			"W: Some index files failed to download. They have been ignored, or old ones used instead.\n"
		wrapperFrames = "\tat java.base/sun.nio.ch.NioSocketImpl.connect(NioSocketImpl.java:567)\n\tat java.base/sun.net.www.protocol.https.HttpsURLConnectionImpl.getInputStream(HttpsURLConnectionImpl.java:223)\n" +
			"\tat org.gradle.wrapper.Download.downloadInternal(Download.java:129)\n\tat org.gradle.wrapper.Download.download(Download.java:109)\n\tat org.gradle.wrapper.Install.forceFetch(Install.java:171)\n\tat org.gradle.wrapper.GradleWrapperMain.main(GradleWrapperMain.java:67)\n"
		mavenWarned = "[WARNING] Could not transfer metadata /.meta/prefixes.txt from/to central (https://repo.maven.apache.org/maven2): repo.maven.apache.org: Temporary failure in name resolution\n"
		mavenFailed = "[INFO] BUILD FAILURE\n[INFO] ------------------------------------------------------------------------\n"
		mavenGone   = "[WARNING] Could not transfer metadata org.example:lib/maven-metadata.xml from/to old (https://old.example/maven): old.example: Temporary failure in name resolution\n"
		mavenEnded  = "[ERROR] Failed to execute goal on project app: Could not resolve dependencies for project org.example:app:jar:1.0: Could not transfer artifact org.example:lib:pom:1.0 from/to "
		aptArchive  = "E: Failed to fetch http://deb.debian.org/debian/pool/main/j/jq/jq_1.7.1-6_amd64.deb  "
		aptNoFetch  = "E: Unable to fetch some archives, maybe run apt-get update or try with --fix-missing?\n"
		mavenAbsent = "[ERROR] Plugin org.apache.maven.plugins:maven-resources-plugin:3.5.0 or one of its dependencies could not be resolved:\n" +
			"[ERROR] \tThe following artifacts could not be resolved: org.apache.maven.plugins:maven-resources-plugin:pom:3.5.0 (absent): Could not transfer artifact org.apache.maven.plugins:maven-resources-plugin:pom:3.5.0 from/to central (https://repo.maven.apache.org/maven2): repo.maven.apache.org\n[ERROR] -> [Help 1]\n"
	)

	for name, scenario := range map[string]struct {
		report string
		want   error
	}{
		"apt: no index, so no package":       {errs.AptReport(aptUnresolved + "Reading package lists...\nE: Unable to locate package buildah\n"), errs.ErrDependencyUnavailable},
		"apt: an archive that timed out":     {errs.AptReport(aptArchive + "Could not connect to deb.debian.org:80 (151.101.86.132), connection timed out\n" + aptNoFetch), errs.ErrDependencyUnavailable},
		"apt: a wrong name, a source unread": {errs.AptReport(aptUnresolved + "E: Unable to locate package buildahh\n"), errs.ErrDependencyUnavailable},
		"apt: a hash that does not match":    {errs.AptReport(aptUnresolved + aptArchive + "Hash Sum mismatch\n" + aptNoFetch), errs.ErrValidation},
		"apt: a signature apt-get refuses":   {errs.AptReport(aptUnresolved + "W: Failed to fetch https://packages.example/debian/dists/stable/InRelease  The following signatures were invalid: EXPKEYSIG 1A2B3C4D5E6F7A8B Example Archive <archive@packages.example>\nE: Unable to locate package buildah\n"), errs.ErrValidation},
		"apt: a package that does not exist": {errs.AptReport("Reading package lists...\nE: Unable to locate package buildahh\n"), errs.ErrValidation},
		"apt: a package script that failed":  {errs.AptReport(aptUnresolved + "Setting up jq (1.7.1-6) ...\nE: Sub-process /usr/bin/dpkg returned an error code (1)\n"), errs.ErrValidation},
		"apt: an archive that answered 404":  {errs.AptReport(aptUnresolved + aptArchive + "404  Not Found [IP: 151.101.86.132 80]\n" + aptNoFetch), errs.ErrValidation},

		"gradle: the wrapper's host did not resolve": {errs.GradleWrapperReport("Downloading https://services.gradle.org/distributions/gradle-9.1.0-bin.zip\n\nException in thread \"main\" java.net.UnknownHostException: services.gradle.org\n" + wrapperFrames), errs.ErrDependencyUnavailable},
		"gradle: the wrapper's download timed out":   {errs.GradleWrapperReport("Exception in thread \"main\" java.io.IOException: Downloading from https://services.gradle.org/distributions/gradle-9.1.0-bin.zip failed: timeout (10000ms)\n" + wrapperFrames + "Caused by: java.net.SocketTimeoutException: Read timed out\n\t... 9 more\n"), errs.ErrDependencyUnavailable},
		"gradle: a program's own exception":          {errs.GradleWrapperReport("Exception in thread \"main\" java.net.ConnectException: Connection refused\n\tat java.base/sun.nio.ch.Net.pollConnect(Native Method)\n\tat com.example.Client.main(Client.java:12)\n"), errs.ErrValidation},
		"gradle: a dependency Gradle did not get":    {errs.GradleWrapperReport("FAILURE: Build failed with an exception.\n\n* What went wrong:\nExecution failed for task ':compileJava'.\n> Could not resolve all files for configuration ':compileClasspath'.\n   > Could not GET 'https://repo.maven.apache.org/maven2/org/example/lib/1.0/lib-1.0.pom'.\n      > repo.maven.apache.org: Temporary failure in name resolution\n"), errs.ErrValidation},

		"maven: a plugin from an unresolved host": {errs.MavenReport(mavenWarned + mavenFailed + mavenAbsent), errs.ErrDependencyUnavailable},
		"maven: failed tests after a warning":     {errs.MavenReport(mavenWarned + "[ERROR] Tests run: 3, Failures: 1, Errors: 0, Skipped: 0\n" + mavenFailed + "[ERROR] Failed to execute goal org.apache.maven.plugins:maven-surefire-plugin:3.5.2:test (default-test) on project app: There are test failures.\n"), errs.ErrValidation},
		"maven: a test that printed a transfer":   {errs.MavenReport("[ERROR]   ResolverTest.fetches:41 Could not transfer artifact org.example:lib:pom:1.0 from/to central (https://repo.maven.apache.org/maven2): repo.maven.apache.org: Temporary failure in name resolution\n" + mavenFailed + "[ERROR] Failed to execute goal org.apache.maven.plugins:maven-surefire-plugin:3.5.2:test (default-test) on project app: There are test failures.\n"), errs.ErrValidation},
		"maven: a repository that answered 401":   {errs.MavenReport(mavenWarned + mavenFailed + "[ERROR] Failed to execute goal on project app: Could not resolve dependencies for project org.example:app:jar:1.0: Could not transfer artifact org.example:lib:pom:1.0 from/to internal (https://repo.example/maven): status code: 401, reason phrase: Unauthorized (401) -> [Help 1]\n"), errs.ErrValidation},
		"maven: a build that did not fail":        {errs.MavenReport(mavenWarned + "[INFO] BUILD SUCCESS\n"), errs.ErrValidation},
		"maven: the reason on the last error":     {errs.MavenReport(mavenFailed + mavenEnded + "central (https://repo.maven.apache.org/maven2): repo.maven.apache.org: Temporary failure in name resolution -> [Help 1]\n"), errs.ErrDependencyUnavailable},
		"maven: another repository's warning":     {errs.MavenReport(mavenGone + mavenFailed + mavenAbsent), errs.ErrValidation},
		"maven: a checksum that does not match":   {errs.MavenReport(mavenGone + mavenFailed + mavenEnded + "old (https://old.example/maven): Checksum validation failed, expected 2ef7bde608ce5404e97d5f042f95f89f1c232871 but is da39a3ee5e6b4b0d3255bfef95601890afd80709 -> [Help 1]\n"), errs.ErrValidation},
		"maven: a certificate Java refuses":       {errs.MavenReport(mavenGone + mavenFailed + mavenEnded + "old (https://old.example/maven): PKIX path building failed: sun.security.provider.certpath.SunCertPathBuilderException: unable to find valid certification path to requested target -> [Help 1]\n"), errs.ErrValidation},
		"maven: a deploy the repository refused":  {errs.MavenReport(mavenGone + mavenFailed + "[ERROR] Failed to execute goal org.apache.maven.plugins:maven-deploy-plugin:3.1.4:deploy (default-deploy) on project app: Failed to deploy artifacts: Could not transfer artifact org.example:app:jar:1.0 from/to old (https://old.example/maven): status: 409 Conflict -> [Help 1]\n"), errs.ErrValidation},
		"maven: a reason that names no host":      {errs.MavenReport(mavenGone + mavenFailed + mavenEnded + "old (https://old.example/maven): transfer failed for https://old.example/maven/org/example/lib/1.0/lib-1.0.pom -> [Help 1]\n"), errs.ErrValidation},
		"maven: a mirror Maven blocks":            {errs.MavenReport(mavenGone + mavenFailed + mavenEnded + "maven-default-http-blocker (http://0.0.0.0/): Blocked mirror for repositories: [old (http://old.example/maven, default, releases+snapshots)] -> [Help 1]\n"), errs.ErrValidation},

		"kaniko: a base image's registry":  {errs.KanikoReport("INFO[0000] Retrieving image manifest alpine:3.22\nerror building image: unable to complete operation after 0 attempts, last error: Get \"https://index.docker.io/v2/\": dial tcp: lookup index.docker.io: i/o timeout\n"), errs.ErrDependencyUnavailable},
		"kaniko: a RUN step's own failure": {errs.KanikoReport("INFO[0003] Running: [/bin/sh -c ./fetch]\nerror building image: dial tcp: lookup mirror.example: i/o timeout\nerror building image: error building stage: failed to execute command: waiting for process to exit: exit status 1\n"), errs.ErrValidation},

		"git: a host that did not resolve":    {errs.GitReport("fatal: unable to access 'https://github.com/org/repo/': Could not resolve host: github.com\n"), errs.ErrDependencyUnavailable},
		"git: a host that refused":            {errs.GitReport("fatal: unable to access 'https://forge.example/org/repo/': Failed to connect to forge.example port 443 after 3 ms: Couldn't connect to server\n"), errs.ErrDependencyUnavailable},
		"git: ssh that timed out":             {errs.GitReport("ssh: connect to host forge.example port 22: Connection timed out\nfatal: Could not read from remote repository.\n"), errs.ErrDependencyUnavailable},
		"git: ssh with a name it cannot find": {errs.GitReport("ssh: Could not resolve hostname forge.example: Name or service not known\nfatal: Could not read from remote repository.\n"), errs.ErrDependencyUnavailable},
		"git: a remote that answered 403":     {errs.GitReport("fatal: unable to access 'https://forge.example/org/repo/': The requested URL returned error: 403\n"), errs.ErrValidation},
		"git: what the remote's hook said":    {errs.GitReport("remote: fatal: unable to access 'https://mirror.example/': Could not resolve host: mirror.example\nerror: failed to push some refs to 'https://forge.example/org/repo'\n"), errs.ErrValidation},
	} {
		if got := errs.ToolFailure(scenario.report); !errors.Is(got, scenario.want) {
			t.Errorf("%s: ToolFailure(%q) = %v, want %v", name, scenario.report, got, scenario.want)
		}
	}
}

// A request the network stopped is transport; a certificate that does not
// verify, a TLS alert and an operating-system error are not, though some of
// them travel as network errors.
func TestIsTransportLeavesOutCertificatesAndAlerts(t *testing.T) {
	t.Parallel()

	for name, scenario := range map[string]struct {
		err  error
		want bool
	}{
		"a name that does not resolve": {&url.Error{Op: "Get", URL: "https://x.example", Err: &net.OpError{Op: "dial", Err: &net.DNSError{Err: "no such host", Name: "x.example", IsNotFound: true}}}, true},
		"a refused connection":         {&url.Error{Op: "Get", URL: "https://x.example", Err: &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}}, true},
		"an unknown authority":         {&url.Error{Op: "Get", URL: "https://x.example", Err: &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}}, false},
		"a wrong host name":            {&url.Error{Op: "Get", URL: "https://x.example", Err: x509.HostnameError{Host: "x.example"}}, false},
		"a TLS alert from the peer":    {&url.Error{Op: "Get", URL: "https://x.example", Err: &net.OpError{Op: "remote error", Err: errors.New("tls: bad certificate")}}, false},
		"a missing file":               {&os.PathError{Op: "open", Path: "/auth.json", Err: syscall.ENOENT}, false},
		"an unknown error":             {errors.New("boom"), false},
	} {
		if got := errs.IsTransport(scenario.err); got != scenario.want {
			t.Errorf("%s: IsTransport = %v, want %v", name, got, scenario.want)
		}
	}
}
