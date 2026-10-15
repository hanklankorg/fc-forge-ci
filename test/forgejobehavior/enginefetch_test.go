// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"bytes"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/evidence"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// packageForge is a forge's generic package registry over HTTPS, as far as
// the layer uses it: it serves one body at every path it is asked for,
// accepts uploads with a status of the test's choosing, and records each
// request as "METHOD path". curl trusts it through the bundle file.
type packageForge struct {
	server *httptest.Server
	bundle string

	mu       sync.Mutex
	requests []string
	headers  []http.Header
	uploaded []byte
}

func newPackageForge(t *testing.T, body string, getStatus, putStatus int) *packageForge {
	t.Helper()

	forge := &packageForge{}
	forge.server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		forge.mu.Lock()
		defer forge.mu.Unlock()

		forge.requests = append(forge.requests, request.Method+" "+request.URL.EscapedPath())
		forge.headers = append(forge.headers, request.Header.Clone())

		if request.Method == http.MethodPut {
			forge.uploaded, _ = io.ReadAll(request.Body)

			writer.WriteHeader(putStatus)

			return
		}

		writer.WriteHeader(getStatus)

		if getStatus == http.StatusOK {
			_, _ = io.WriteString(writer, body)
		}
	}))
	t.Cleanup(forge.server.Close)

	forge.bundle = filepath.Join(t.TempDir(), "ca.pem")

	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: forge.server.Certificate().Raw})
	if err := os.WriteFile(forge.bundle, certificate, 0o600); err != nil {
		t.Fatal(err)
	}

	return forge
}

func (f *packageForge) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.requests...)
}

// fetchSandbox is a helper checkout as a job has it: the real resolver, the
// recorded checksum of a fixture engine, and no binary. It returns the
// engine's text, which a forge serves, and its digest.
func fetchSandbox(t *testing.T) (*sandbox, string, string) {
	t.Helper()

	box, digest, _ := setupForgeCISandbox(t, `printf 'ran %s\n' "$*"`)
	engine := readSandboxFile(t, box, ".forgejo/bin/forge-ci")

	if err := os.Remove(filepath.Join(box.root, ".forgejo", "bin", "forge-ci")); err != nil {
		t.Fatal(err)
	}

	return box, engine, digest
}

const (
	sourceResolver = `source "${GITHUB_ACTION_PATH}/../../bin/lib-forge-ci.sh"` + "\n"
	packagePath    = "/generic/forge-ci/"
	packageFile    = "/forge-ci-linux-amd64"
)

// The binary is not in the checkout. The resolver downloads it by the
// recorded checksum from the generic packages of the owner of the repository
// that serves the layer, and two rules name that repository: the runner's own
// account of where it fetched an action from, and otherwise the repository
// the workflow says it cloned. The place the binary lands follows the same
// split, because an action fetched by URL has a directory that does not
// outlive its step.
func TestResolverDownloadsTheBinaryByItsRecordedChecksum(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		// env returns the coordinates of the step, given the forge's URL.
		env func(forge string) []string
		// wantPath is the request path before the digest, and lands the
		// place of the binary relative to the sandbox root, with DIGEST
		// standing for the digest.
		wantPath, lands string
	}{
		{
			name: "a checkout the workflow made: the repository it cloned, into the checkout",
			env: func(forge string) []string {
				return []string{"FORGE_CI_REPOSITORY_URL=" + forge + "/acme/delivery.git"}
			},
			wantPath: "/api/packages/acme", lands: ".forgejo/bin/forge-ci",
		},
		{
			name: "a forge under a path, and a repository named without .git",
			env: func(forge string) []string {
				return []string{"FORGE_CI_REPOSITORY_URL=" + forge + "/code/acme/delivery/"}
			},
			wantPath: "/code/api/packages/acme", lands: ".forgejo/bin/forge-ci",
		},
		{
			name: "an action fetched by URL: the runner's repository, into the job's temporary directory",
			env: func(forge string) []string {
				return []string{
					"GITHUB_SERVER_URL=" + forge, "GITHUB_ACTION_REPOSITORY=fork/delivery-0a1b",
					// The consumer's own variable does not name the engine.
					"FORGE_CI_REPOSITORY_URL=" + forge + "/consumer/elsewhere.git",
				}
			},
			wantPath: "/api/packages/fork", lands: "runner-temp/forge-ci-DIGEST/forge-ci",
		},
		{
			name: "GitLab uses the full project path, including subgroups",
			env: func(forge string) []string {
				return []string{"FORGE_CI_PACKAGE_PROVIDER=gitlab", "CI_SERVER_URL=" + forge,
					"FORGE_CI_REPOSITORY_URL=" + forge + "/acme/team/delivery.git", "CI_JOB_TOKEN=not-for-downloads"}
			},
			wantPath: "/api/v4/projects/acme%2Fteam%2Fdelivery/packages", lands: ".forgejo/bin/forge-ci",
		},
		{
			name: "GitLab under a relative URL root",
			env: func(forge string) []string {
				return []string{"FORGE_CI_PACKAGE_PROVIDER=gitlab", "CI_SERVER_URL=" + forge + "/code",
					"FORGE_CI_REPOSITORY_URL=" + forge + "/code/acme/delivery.git"}
			},
			wantPath: "/code/api/v4/projects/acme%2Fdelivery/packages", lands: ".forgejo/bin/forge-ci",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			box, engine, digest := fetchSandbox(t)
			forge := newPackageForge(t, engine, http.StatusOK, http.StatusCreated)
			env := append(test.env(forge.server.URL), "CURL_CA_BUNDLE="+forge.bundle, "RUNNER_TEMP="+filepath.Join(box.root, "runner-temp"))

			if err := os.Mkdir(filepath.Join(box.root, "runner-temp"), 0o700); err != nil {
				t.Fatal(err)
			}

			// Two steps of one job: the second finds what the first fetched.
			for step := range 2 {
				got := box.run(sourceResolver+"run_forge_ci release sign\n", env...)
				if got.status != 0 || got.stdout != "ran release sign\n" {
					t.Fatalf("step %d: status %d stdout %q stderr %s", step, got.status, got.stdout, got.stderr)
				}
			}

			want := []string{"GET " + test.wantPath + packagePath + digest + packageFile}
			if seen := forge.seen(); len(seen) != 1 || seen[0] != want[0] {
				t.Fatalf("the forge was asked %q, want once %q", seen, want)
			}

			if authorization := forge.headers[0].Get("Authorization"); authorization != "" {
				t.Errorf("the download carried a credential: %q", authorization)
			}

			if forge.headers[0].Get("Job-Token") != "" || forge.headers[0].Get("Private-Token") != "" {
				t.Error("the binary download carried a GitLab credential")
			}

			landed := filepath.Join(box.root, filepath.FromSlash(strings.ReplaceAll(test.lands, "DIGEST", digest)))
			if info, err := os.Stat(landed); err != nil || info.Mode().Perm()&0o111 == 0 {
				t.Fatalf("no executable binary at %s: %v", landed, err)
			}

			if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(landed), ".forge-ci.*")); len(leftovers) != 0 {
				t.Errorf("the download left temporary files: %q", leftovers)
			}
		})
	}
}

// A consumer's job uses setup-forge-ci by URL. The action's directory is
// gone after the step, so what it puts on the job's PATH, and names in its
// output, is the directory in the job's temporary directory the binary was
// downloaded to.
func TestSetupForgeCIExposesTheDownloadedBinary(t *testing.T) {
	t.Parallel()

	box, engine, digest := fetchSandbox(t)
	forge := newPackageForge(t, engine, http.StatusOK, http.StatusCreated)
	pathFile := box.write("path", "", 0o600)
	temp := filepath.Join(box.root, "runner-temp")

	if err := os.Mkdir(temp, 0o700); err != nil {
		t.Fatal(err)
	}

	got := box.run(actionStep(t, "setup-forge-ci", "setup"),
		"GITHUB_SERVER_URL="+forge.server.URL, "GITHUB_ACTION_REPOSITORY=fork/delivery-0a1b", "RUNNER_TEMP="+temp,
		"CURL_CA_BUNDLE="+forge.bundle, "FORGEJO_PATH="+pathFile, "EXPECTED_SHA256="+digest)
	if got.status != 0 || got.stdout != "ran --version sha256:"+digest+"\n" {
		t.Fatalf("status %d stdout %q; stderr %s", got.status, got.stdout, got.stderr)
	}

	directory := filepath.Join(temp, "forge-ci-"+digest)
	if path := readSandboxFile(t, box, "path"); path != directory+"\n" {
		t.Errorf("the job PATH gained %q, want %s", path, directory)
	}

	if want := "binary-path=" + directory + "/forge-ci\nsha256=" + digest + "\nversion=ran --version\n"; box.output() != want {
		t.Errorf("outputs = %q, want %q", box.output(), want)
	}

	if seen := forge.seen(); len(seen) != 1 || seen[0] != "GET /api/packages/fork"+packagePath+digest+packageFile {
		t.Errorf("the forge was asked %q", seen)
	}
}

// A download that cannot be had, or is not the recorded bytes, stops the step
// that asked for it with the address, and puts nothing in place. A missing
// package also says which workflow publishes it.
func TestResolverRefusesWhatItCannotVerify(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		status int
		body   func(engine string) string
		env    func(forge string) []string
		// asked says whether the forge is reached at all.
		asked bool
		say   []string
	}{
		{
			name: "the package is not published", status: http.StatusNotFound, asked: true,
			env: func(forge string) []string {
				return []string{"FORGE_CI_REPOSITORY_URL=" + forge + "/acme/delivery.git"}
			},
			say: []string{"cannot download the forge-ci binary", "/api/packages/acme/generic/forge-ci/", "examples/forgejo/signer-image.yml", "Run it at this commit"},
		},
		{
			name: "the package is other bytes", status: http.StatusOK, asked: true,
			body: func(engine string) string { return engine + "# changed in storage\n" },
			env: func(forge string) []string {
				return []string{"FORGE_CI_REPOSITORY_URL=" + forge + "/acme/delivery.git"}
			},
			say: []string{"is not the one the pinned commit records", "/api/packages/acme/generic/forge-ci/", "recorded:", "actual:"},
		},
		{
			name: "nothing names the serving repository", status: http.StatusOK,
			env: func(string) []string { return nil },
			say: []string{"cannot tell which repository serves the forge-ci layer", "FORGE_CI_REPOSITORY_URL"},
		},
		{
			name: "the serving repository is not HTTPS", status: http.StatusOK,
			env: func(forge string) []string {
				return []string{"FORGE_CI_REPOSITORY_URL=" + strings.Replace(forge, "https://", "http://", 1) + "/acme/delivery.git"}
			},
			say: []string{"must be an HTTPS URL ending in owner/repository", "http://"},
		},
		{
			name: "the serving repository carries a credential", status: http.StatusOK,
			env: func(forge string) []string {
				return []string{"FORGE_CI_REPOSITORY_URL=" + strings.Replace(forge, "https://", "https://user:secret@", 1) + "/acme/delivery.git"}
			},
			say: []string{"must be an HTTPS URL ending in owner/repository"},
		},
		{
			name: "GitLab component repository is on another instance", status: http.StatusOK,
			env: func(forge string) []string {
				return []string{"FORGE_CI_PACKAGE_PROVIDER=gitlab", "CI_SERVER_URL=" + forge,
					"FORGE_CI_REPOSITORY_URL=https://different.invalid/acme/delivery.git"}
			},
			say: []string{"GitLab helpers must name a project on CI_SERVER_URL"},
		},
		{
			name: "GitLab project path climbs out of its namespace", status: http.StatusOK,
			env: func(forge string) []string {
				return []string{"FORGE_CI_PACKAGE_PROVIDER=gitlab", "CI_SERVER_URL=" + forge,
					"FORGE_CI_REPOSITORY_URL=" + forge + "/acme/../delivery.git"}
			},
			say: []string{"GitLab helpers must name a project on CI_SERVER_URL"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			box, engine, _ := fetchSandbox(t)
			if test.body != nil {
				engine = test.body(engine)
			}

			forge := newPackageForge(t, engine, test.status, http.StatusCreated)

			got := box.run(sourceResolver+"run_forge_ci release sign\n",
				append(test.env(forge.server.URL), "CURL_CA_BUNDLE="+forge.bundle)...)
			if got.status != 69 || got.stdout != "" {
				t.Fatalf("status %d stdout %q, want a refusal with 69; stderr %s", got.status, got.stdout, got.stderr)
			}

			for _, fragment := range test.say {
				if !strings.Contains(got.stderr, fragment) {
					t.Errorf("the refusal lacks %q:\n%s", fragment, got.stderr)
				}
			}

			if asked := len(forge.seen()) > 0; asked != test.asked {
				t.Errorf("the forge was asked: %t, want %t (%q)", asked, test.asked, forge.seen())
			}

			if entries, _ := filepath.Glob(filepath.Join(box.root, ".forgejo", "bin", "*forge-ci")); len(entries) != 0 {
				t.Errorf("a refused download left %q in place", entries)
			}
		})
	}
}

// A privileged job never downloads on the way to an engine call: that call
// may hold a signing key. It downloads in the step that exists for it, and a
// job that skipped the step stops at its first engine call, and before that
// at the toolchain check.
func TestStrictModeDownloadsOnlyInTheFetchStep(t *testing.T) {
	t.Parallel()

	box, engine, digest := fetchSandbox(t)
	forge := newPackageForge(t, engine, http.StatusOK, http.StatusCreated)
	env := []string{
		"SIGNER_STRICT=1", "FORGE_CI_REPOSITORY_URL=" + forge.server.URL + "/acme/delivery.git", "CURL_CA_BUNDLE=" + forge.bundle,
		"HELPERS_DIR=" + box.root,
	}

	got := box.run(sourceResolver+"run_forge_ci release sign\n", env...)
	if got.status != 69 || !strings.Contains(got.stderr, "forge-ci binary is missing") || len(forge.seen()) != 0 {
		t.Fatalf("an engine call in strict mode: status %d, forge asked %q; stderr %s", got.status, forge.seen(), got.stderr)
	}

	// The step as every signer job has it, and then an engine call.
	step := workflowStep(t, "sign-container-images.yml", "sign-container-images", "Fetch engine binary")
	if len(step.Env) != 0 {
		t.Fatalf("the fetch step sets %v; it reads the job's FORGE_CI_REPOSITORY_URL and no secret", step.Env)
	}

	for _, script := range []string{step.Run, sourceResolver + "run_forge_ci release sign\n"} {
		if got = box.run(script, env...); got.status != 0 {
			t.Fatalf("status %d; stderr %s", got.status, got.stderr)
		}
	}

	if seen := forge.seen(); got.stdout != "ran release sign\n" || len(seen) != 1 || !strings.HasSuffix(seen[0], packagePath+digest+packageFile) {
		t.Fatalf("stdout %q, forge asked %q, want one download and the engine run", got.stdout, seen)
	}
}

// publishScript runs `forgejo-binary.sh publish` on a file, against a forge.
func publishScript(t *testing.T, forge *packageForge, binary string, env ...string) (int, string) {
	t.Helper()

	var output bytes.Buffer

	command := exec.CommandContext(t.Context(), "bash", filepath.Join(reporoot.Path(t), binaryScript), "publish", binary) //nolint:gosec // G204: this repository's own script.
	command.Stdout, command.Stderr = &output, &output

	command.Env = append([]string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir(),
		"CURL_CA_BUNDLE=" + forge.bundle, "FORGE_CI_REPOSITORY_URL=" + forge.server.URL + "/acme/delivery.git",
	}, env...)

	err := command.Run()

	exit := &exec.ExitError{}
	if errors.As(err, &exit) {
		return exit.ExitCode(), output.String()
	} else if err != nil {
		t.Fatalf("run %s: %v", binaryScript, err)
	}

	return 0, output.String()
}

// Publishing uploads only the build the tree records, to the generic packages
// of the engine repository's owner, with the token in a header and in no
// output, and then downloads it as a job does. A version that is already
// there cannot be replaced and is not an error: what counts is that the
// download verifies.
func TestPublishUploadsOnlyTheRecordedBuild(t *testing.T) {
	t.Parallel()

	const token = "publish-token-0123456789"

	t.Run("refuses a build the tree does not record", func(t *testing.T) {
		t.Parallel()

		forge := newPackageForge(t, "", http.StatusOK, http.StatusCreated)
		other := filepath.Join(t.TempDir(), "forge-ci")

		if err := os.WriteFile(other, []byte("not the recorded build\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		status, output := publishScript(t, forge, other, "FORGE_CI_PACKAGE_TOKEN="+token)
		if status != 1 || !strings.Contains(output, "is not the one the tree records") || len(forge.seen()) != 0 {
			t.Fatalf("status %d, forge asked %q\n%s", status, forge.seen(), output)
		}
	})

	engine := forgeCIEngine(t)

	recorded := recordedChecksum(t)

	if fileSHA256(t, engine) != recorded {
		evidence.NotProvenf(t, "the build of this tree is not the recorded one, so there is nothing the script would publish")
	}

	body, err := os.ReadFile(engine) //nolint:gosec // the engine this package built.
	if err != nil {
		t.Fatal(err)
	}

	want := "/api/packages/acme" + packagePath + recorded + packageFile

	for _, test := range []struct {
		name      string
		putStatus int
		getStatus int
		token     string
		status    int
		say       string
	}{
		{name: "uploads and reads back", putStatus: http.StatusCreated, getStatus: http.StatusOK, token: token, say: "Published and verified"},
		{name: "accepts a version that is already there", putStatus: http.StatusConflict, getStatus: http.StatusOK, token: token, say: "Already published"},
		{name: "fails on a refused token", putStatus: http.StatusUnauthorized, getStatus: http.StatusOK, token: token, status: 1, say: "upload answered HTTP 401"},
		{name: "fails when the package cannot be read without credentials", putStatus: http.StatusCreated, getStatus: http.StatusNotFound, token: token, status: 1, say: "cannot download the forge-ci binary"},
		{name: "needs a token", putStatus: http.StatusCreated, getStatus: http.StatusOK, status: 1, say: "FORGE_CI_PACKAGE_TOKEN is empty"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			forge := newPackageForge(t, string(body), test.getStatus, test.putStatus)

			status, output := publishScript(t, forge, engine, "FORGE_CI_PACKAGE_TOKEN="+test.token)
			if status != test.status || !strings.Contains(output, test.say) {
				t.Fatalf("status %d, want %d with %q\n%s", status, test.status, test.say, output)
			}

			if strings.Contains(output, token) {
				t.Fatalf("the token is in the output:\n%s", output)
			}

			if test.token == "" {
				if seen := forge.seen(); len(seen) != 0 {
					t.Fatalf("the forge was asked %q without a token", seen)
				}

				return
			}

			requirePublishRequests(t, forge, want, token, body, test.status == 0 || test.getStatus != http.StatusOK)

			if !strings.Contains(output, forge.server.URL+want) {
				t.Errorf("the output does not name the address:\n%s", output)
			}
		})
	}
}

// requirePublishRequests holds what the forge saw to one upload of body to
// path with the token and, when readBack, one download after it without a
// credential.
func requirePublishRequests(t *testing.T, forge *packageForge, path, token string, body []byte, readBack bool) {
	t.Helper()

	seen := forge.seen()
	if len(seen) == 0 || seen[0] != "PUT "+path || forge.headers[0].Get("Authorization") != "token "+token || !bytes.Equal(forge.uploaded, body) {
		t.Fatalf("the upload was %q, want the recorded build to %s with the token", seen, path)
	}

	if !readBack {
		return
	}

	if len(seen) < 2 || seen[1] != "GET "+path || forge.headers[1].Get("Authorization") != "" {
		t.Fatalf("the read-back was %q, want %s without a credential", seen, path)
	}
}

func TestPublishGitLabUsesTheSelectedTokenAndAnonymousReadBack(t *testing.T) {
	t.Parallel()

	engine := forgeCIEngine(t)

	recorded := recordedChecksum(t)
	if fileSHA256(t, engine) != recorded {
		evidence.NotProvenf(t, "the current engine build differs from the recorded checksum")
	}

	body, err := os.ReadFile(engine) //nolint:gosec // the engine this package built.
	if err != nil {
		t.Fatal(err)
	}

	const token = "publish-token-0123456789"

	for tokenType, header := range map[string]string{"private": "Private-Token", "job": "Job-Token", "invalid": ""} {
		t.Run(tokenType, func(t *testing.T) {
			t.Parallel()

			forge := newPackageForge(t, string(body), http.StatusOK, http.StatusCreated)

			status, output := publishScript(t, forge, engine,
				"FORGE_CI_PACKAGE_PROVIDER=gitlab", "CI_SERVER_URL="+forge.server.URL,
				"FORGE_CI_PACKAGE_TOKEN_TYPE="+tokenType, "FORGE_CI_PACKAGE_TOKEN="+token)
			if strings.Contains(output, token) {
				t.Fatal("the package token reached the output")
			}

			seen := forge.seen()
			if header == "" {
				if status != 1 || len(seen) != 0 {
					t.Fatalf("invalid token type reached the forge: status=%d requests=%v", status, seen)
				}

				return
			}

			want := "/api/v4/projects/acme%2Fdelivery/packages" + packagePath + recorded + packageFile
			if status != 0 || len(seen) != 2 || seen[0] != "PUT "+want || seen[1] != "GET "+want || !bytes.Equal(forge.uploaded, body) {
				t.Fatalf("GitLab publication: status=%d requests=%v\n%s", status, seen, output)
			}

			if forge.headers[0].Get(header) != token || forge.headers[0].Get("Authorization") != "" {
				t.Fatal("GitLab upload used the wrong authentication scheme")
			}

			if forge.headers[1].Get("Private-Token") != "" || forge.headers[1].Get("Job-Token") != "" || forge.headers[1].Get("Authorization") != "" {
				t.Fatal("GitLab read-back was not anonymous")
			}
		})
	}
}
