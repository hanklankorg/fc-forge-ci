// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package kaniko

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
)

// fakeExecutor stands in for /kaniko/executor: it prints its argv and the
// credentials directory kaniko would read, and records the config.json it finds
// there, so the test sees what kaniko would have seen.
func fakeExecutor(t *testing.T) *Adapter {
	t.Helper()

	bins := mockbinary.New(t)
	bins.Add("executor", `printf 'ARGV %s\n' "$*"; printf 'DOCKER_CONFIG=%s\n' "${DOCKER_CONFIG:-<unset>}"; if [ -n "${DOCKER_CONFIG:-}" ]; then printf 'CONFIG %s\n' "$(cat "$DOCKER_CONFIG/config.json")"; fi`)

	return &Adapter{Executor: bins.Path("executor"), euid: func() int { return 0 }}
}

func TestExecutorRestrictsTheInheritedEnvironment(t *testing.T) {
	t.Setenv("SIGSTORE_ROOT_FILE", "untrusted")
	t.Setenv("TUF_ROOT", "untrusted")
	t.Setenv("COSIGN_REPOSITORY", "untrusted")
	t.Setenv("GPG_PRIVATE_KEY", "unrelated")
	t.Setenv("SSL_CERT_FILE", "/runner/ca.pem")
	t.Setenv("HTTPS_PROXY", "http://proxy.example")
	bins := mockbinary.New(t)
	bins.Add("executor", `
[ -z "${SIGSTORE_ROOT_FILE+set}${TUF_ROOT+set}${COSIGN_REPOSITORY+set}${GPG_PRIVATE_KEY+set}" ] || exit 31
[ "${SSL_CERT_FILE-}" = /runner/ca.pem ] || exit 32
[ "${HTTPS_PROXY-}" = http://proxy.example ] || exit 33
`)
	adapter := &Adapter{Executor: bins.Path("executor"), euid: func() int { return 0 }}

	if err := adapter.BuildToLayout(t.Context(), container.BuildRequest{
		Context: t.TempDir(), Mode: container.BuildModePushByDigest, ImageRef: "registry.example/app",
	}, t.TempDir(), io.Discard); err != nil {
		t.Fatalf("executor subprocess environment: %v", err)
	}
}

func TestBuildToLayout_ArgvShape(t *testing.T) {
	t.Parallel()

	adapter := fakeExecutor(t)
	authFile := filepath.Join(t.TempDir(), "auth.json")

	if err := os.WriteFile(authFile, []byte(`{"auths":{"ghcr.io":{"auth":"Y2FuYXJ5"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	layout := filepath.Join(t.TempDir(), "layout")

	var log strings.Builder

	err := adapter.BuildToLayout(t.Context(), container.BuildRequest{
		AuthFile: authFile, Context: ".", Containerfile: "Containerfile", Target: "runtime", Platform: "linux/arm64",
		BuildArgs: []string{"VERSION=1.2.3"}, Labels: []string{"org.opencontainers.image.revision=abc"},
		SourceDateEpoch: "1700000000", CacheRepo: "ghcr.io/org/cache", CachePush: false,
		Mode: container.BuildModePushByDigest, ImageRef: "ghcr.io/org/app",
	}, layout, &log)
	if err != nil {
		t.Fatal(err)
	}

	cwd, _ := os.Getwd()

	for _, want := range []string{
		"--context dir://" + cwd,
		"--no-push --cleanup",
		"--oci-layout-path " + layout,
		"--ignore-path " + layout,
		"--ignore-path " + cwd,
		"--dockerfile " + filepath.Join(cwd, "Containerfile"),
		"--custom-platform linux/arm64",
		"--target runtime",
		"--build-arg VERSION=1.2.3",
		"--label org.opencontainers.image.revision=abc",
		"--reproducible",
		"--snapshot-mode=full",
		"--image-download-retry 5",
		"--compressed-caching=false",
		"--cache=true --cache-repo ghcr.io/org/cache --no-push-cache",
		`CONFIG {"auths":{"ghcr.io":{"auth":"Y2FuYXJ5"}}}`,
	} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("missing %q:\n%s", want, log.String())
		}
	}

	if strings.Contains(log.String(), "DOCKER_CONFIG=<unset>") {
		t.Error("kaniko was not given the registry credentials")
	}

	if !strings.Contains(log.String(), "replaces this container's root filesystem") {
		t.Error("the log must warn what kaniko does to the job container")
	}
}

func TestBuildToLayout_WithoutCredentialsOrCacheStaysMinimal(t *testing.T) {
	t.Parallel()

	adapter := fakeExecutor(t)

	var log strings.Builder

	err := adapter.BuildToLayout(t.Context(), container.BuildRequest{Context: ".", Mode: container.BuildModeOCI, OutputDir: t.TempDir()}, t.TempDir(), &log)
	if err != nil {
		t.Fatal(err)
	}

	for _, absent := range []string{"--dockerfile", "--custom-platform", "--reproducible", "--cache", "--compressed-caching", "--target", "CONFIG "} {
		if strings.Contains(log.String(), absent) {
			t.Errorf("unexpected %q:\n%s", absent, log.String())
		}
	}

	if !strings.Contains(log.String(), "DOCKER_CONFIG=<unset>") {
		t.Error("no auth file must mean no credentials directory")
	}
}

// What kaniko cannot do is refused by name, never worked around.
func TestAdapter_RefusesWhatNeedsALocalStoreOrSecrets(t *testing.T) {
	t.Parallel()

	adapter := fakeExecutor(t)

	for name, run := range map[string]func() error{
		"load mode": func() error {
			return adapter.Build(t.Context(), container.BuildRequest{Context: ".", Mode: container.BuildModeLoad, ImageRef: "app:test"}, &strings.Builder{})
		},
		"build secrets": func() error {
			return adapter.BuildToLayout(t.Context(), container.BuildRequest{Context: ".", Mode: container.BuildModeOCI, OutputDir: t.TempDir(), Secrets: []string{"id=npm,src=/tmp/npmrc"}}, t.TempDir(), &strings.Builder{})
		},
		"export without a staged build": func() error {
			return adapter.ExportLocalImageToLayout(t.Context(), "localhost/nothing:amd64", t.TempDir(), &strings.Builder{})
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := run()
			if !errors.Is(err, errs.ErrUsage) || !strings.Contains(err.Error(), "kaniko") {
				t.Fatalf("err=%v, want a usage refusal naming kaniko", err)
			}
		})
	}
}

func TestPreflight_NamesTheMissingExecutorAndTheMissingRoot(t *testing.T) {
	t.Parallel()

	missing := &Adapter{Executor: filepath.Join(t.TempDir(), "executor"), euid: func() int { return 0 }}
	if err := missing.BuildToLayout(t.Context(), container.BuildRequest{Context: ".", Mode: container.BuildModeOCI, OutputDir: t.TempDir()}, t.TempDir(), &strings.Builder{}); !errors.Is(err, errs.ErrPrerequisite) || !strings.Contains(err.Error(), "kaniko job image") {
		t.Errorf("missing executor: err=%v", err)
	}

	unprivileged := fakeExecutor(t)
	unprivileged.euid = func() int { return 1000 }

	if err := unprivileged.BuildToLayout(t.Context(), container.BuildRequest{Context: ".", Mode: container.BuildModeOCI, OutputDir: t.TempDir()}, t.TempDir(), &strings.Builder{}); !errors.Is(err, errs.ErrUsage) || !strings.Contains(err.Error(), "uid 0") {
		t.Errorf("non-root: err=%v", err)
	}
}

// The build-arch tool builds into a local tag and exports it. Kaniko has no
// local store, so the build waits for the export and runs once, straight
// into the layout, with the provenance labels the tool expects.
func TestBuildArch_BuildsOnceAtExportWithProvenanceLabels(t *testing.T) {
	t.Parallel()

	adapter := fakeExecutor(t)

	var log strings.Builder

	req := container.SignerImageBuildToolRequest{
		Platform: "linux/amd64", SourceURL: "https://forge.example/org/forge-ci", Revision: "abc123",
		LocalImage: "localhost/signer:amd64", Containerfile: "containers/signer/Containerfile", Context: ".", Title: "signer",
		Target: "runtime", BuildArgs: []string{"FLAVOR=core"}, Labels: []string{"org.example.flavor=core"},
	}
	if err := adapter.BuildSignerImage(t.Context(), req, &log); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(log.String(), "ARGV") {
		t.Fatal("the build must not run before the export names its layout")
	}

	layout := filepath.Join(t.TempDir(), "layout")
	if err := adapter.ExportLocalImageToLayout(t.Context(), "localhost/signer:amd64", layout, &log); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"--oci-layout-path " + layout,
		"--custom-platform linux/amd64",
		"--label org.opencontainers.image.source=https://forge.example/org/forge-ci",
		"--label org.opencontainers.image.revision=abc123",
		"--label org.opencontainers.image.title=signer",
		"--label org.example.flavor=core",
		"--target runtime",
		"--build-arg FLAVOR=core",
		"--reproducible",
	} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("missing %q:\n%s", want, log.String())
		}
	}

	if strings.Count(log.String(), "ARGV") != 1 {
		t.Errorf("want exactly one kaniko run, got:\n%s", log.String())
	}

	// A second export of the same tag has nothing staged.
	if err := adapter.ExportLocalImageToLayout(t.Context(), "localhost/signer:amd64", layout, &log); !errors.Is(err, errs.ErrUsage) {
		t.Errorf("second export: err=%v, want a usage refusal", err)
	}
}

// One job builds every planned image of its architecture with one adapter.
// Each run deletes the filesystem it built on, so the next image starts from
// its own base, not from the previous image's root filesystem.
func TestBuildArch_EachImageOfAJobStartsClean(t *testing.T) {
	t.Parallel()

	adapter := fakeExecutor(t)

	var log strings.Builder

	for _, name := range []string{"app", "worker"} {
		req := container.SignerImageBuildToolRequest{Platform: "linux/amd64", LocalImage: "localhost/" + name + ":amd64", Context: ".", Title: name}
		if err := adapter.BuildSignerImage(t.Context(), req, &log); err != nil {
			t.Fatal(err)
		}

		if err := adapter.ExportLocalImageToLayout(t.Context(), req.LocalImage, filepath.Join(t.TempDir(), name), &log); err != nil {
			t.Fatal(err)
		}
	}

	runs := 0

	for line := range strings.SplitSeq(log.String(), "\n") {
		if !strings.HasPrefix(line, "ARGV ") {
			continue
		}

		runs++

		if !strings.Contains(line, " --cleanup ") {
			t.Errorf("kaniko run %d keeps its filesystem for the next image: %s", runs, line)
		}
	}

	if runs != 2 {
		t.Fatalf("want one kaniko run per image, got %d:\n%s", runs, log.String())
	}
}

func TestStageDockerConfig_RequiresTheAuthsDocument(t *testing.T) {
	t.Parallel()

	notAuths := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(notAuths, []byte("ghp_canary\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := stageDockerConfig(notAuths); !errors.Is(err, errs.ErrInvalidConfig) {
		t.Fatalf("err=%v, want ErrInvalidConfig for a bare token", err)
	}
}

// A Containerfile's own ignore file, else the context's, is what kaniko reads,
// .containerignore ahead of .dockerignore, as Buildah reads them. Kaniko knows
// only .dockerignore names. The executor here reads as kaniko does,
// <dockerfile>.dockerignore else <context>/.dockerignore, and prints what it
// found. The context mirrors a release image that ships a prebuilt binary: the
// root allowlist leaves the binary out, the release Containerfile's admits it.
func TestBuildToLayout_KanikoReadsTheIgnoreFileThatApplies(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		files map[string]string
		want  string
	}{
		"own .containerignore": {
			files: map[string]string{
				".containerignore": "root", ".dockerignore": "root docker",
				"packaging/Containerfile.release.containerignore": "own", "packaging/Containerfile.release.dockerignore": "own docker",
			},
			want: "own",
		},
		"own .dockerignore": {
			files: map[string]string{".containerignore": "root", "packaging/Containerfile.release.dockerignore": "own docker"},
			want:  "own docker",
		},
		"root .containerignore only": {
			files: map[string]string{".containerignore": "root", ".dockerignore": "root docker"},
			want:  "root",
		},
		"none": {want: "<none>"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			contextDir := t.TempDir()
			containerfile := filepath.Join(contextDir, "packaging", "Containerfile.release")

			if tc.files == nil {
				tc.files = map[string]string{}
			}

			tc.files["packaging/Containerfile.release"] = "FROM scratch\n"

			for path, body := range tc.files {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(contextDir, path)), 0o750); err != nil {
					t.Fatal(err)
				}

				if err := os.WriteFile(filepath.Join(contextDir, path), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			bins := mockbinary.New(t)
			bins.Add("executor", `printf 'ARGV %s\n' "$*"; df=Dockerfile ctx=.
while [ $# -gt 0 ]; do case $1 in --dockerfile) df=$2; shift ;; --context) ctx=${2#dir://}; shift ;; esac; shift; done
printf 'DOCKERFILE %s\n' "$df"; printf 'FROM %s\n' "$(cat "$df")"
if [ -f "$df.dockerignore" ]; then printf 'READS %s\n' "$(cat "$df.dockerignore")"
elif [ -f "$ctx/.dockerignore" ]; then printf 'READS %s\n' "$(cat "$ctx/.dockerignore")"
else echo 'READS <none>'; fi`)
			adapter := &Adapter{Executor: bins.Path("executor"), euid: func() int { return 0 }}

			var log strings.Builder

			err := adapter.BuildToLayout(t.Context(), container.BuildRequest{
				Context: contextDir, Containerfile: containerfile, Mode: container.BuildModeOCI, OutputDir: t.TempDir(),
			}, t.TempDir(), &log)
			if err != nil {
				t.Fatal(err)
			}

			if !strings.Contains(log.String(), "READS "+tc.want+"\n") {
				t.Fatalf("kaniko must read %q:\n%s", tc.want, log.String())
			}

			if !strings.Contains(log.String(), "FROM FROM scratch\n") {
				t.Fatalf("kaniko must build the Containerfile itself:\n%s", log.String())
			}

			_, dockerfile, _ := strings.Cut(log.String(), "DOCKERFILE ")
			dockerfile, _, _ = strings.Cut(dockerfile, "\n")

			staged := dockerfile != containerfile
			if wantStaged := strings.HasPrefix(name, "own .containerignore") || strings.HasPrefix(name, "root .containerignore"); staged != wantStaged {
				t.Fatalf("staged=%t, want %t: kaniko reads a .dockerignore unaided and needs a .containerignore staged:\n%s", staged, wantStaged, log.String())
			}

			if !staged {
				return
			}

			if _, err := os.Stat(filepath.Dir(dockerfile)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the staged containerfile must be removed after the build: %v", err)
			}

			if !strings.Contains(log.String(), "--ignore-path "+filepath.Dir(dockerfile)) {
				t.Errorf("the staged containerfile must be kept out of kaniko's way:\n%s", log.String())
			}
		})
	}
}

// layoutExecutor stands in for /kaniko/executor in local mode: it writes the
// prepared layout where --oci-layout-path points, as kaniko would.
func layoutExecutor(t *testing.T, fixture string) *Adapter {
	t.Helper()

	bins := mockbinary.New(t)
	bins.Add("executor", `while [ $# -gt 0 ]; do if [ "$1" = --oci-layout-path ]; then cp -R '`+fixture+`'/. "$2"; fi; shift; done`)

	return &Adapter{Executor: bins.Path("executor"), euid: func() int { return 0 }, Filesystem: ociregistry.NewAnonymous()}
}

func layerOf(t *testing.T, entries ...tar.Header) v1.Layer {
	t.Helper()

	var data bytes.Buffer

	tw := tar.NewWriter(&data)

	for _, hdr := range entries {
		body := ""
		if hdr.Typeflag == tar.TypeReg {
			body = "content of " + hdr.Name
			hdr.Size = int64(len(body))
		}

		if err := tw.WriteHeader(&hdr); err != nil {
			t.Fatal(err)
		}

		if _, err := io.WriteString(tw, body); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	return static.NewLayer(data.Bytes(), types.OCIUncompressedLayer)
}

func layoutFixture(t *testing.T, layers ...v1.Layer) string {
	t.Helper()

	image, err := mutate.AppendLayers(empty.Image, layers...)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()

	path, err := layout.Write(dir, empty.Index)
	if err != nil {
		t.Fatal(err)
	}

	if err := path.AppendImage(image); err != nil {
		t.Fatal(err)
	}

	return dir
}

// Local mode builds to a layout and exports the image's effective
// filesystem: the latest bytes, without what a whiteout deleted.
func TestBuild_LocalModeExportsTheFlattenedFilesystem(t *testing.T) {
	t.Parallel()

	fixture := layoutFixture(t,
		layerOf(t, tar.Header{Name: "hello", Typeflag: tar.TypeReg, Mode: 0o755}, tar.Header{Name: "stale", Typeflag: tar.TypeReg, Mode: 0o644}),
		layerOf(t, tar.Header{Name: ".wh.stale", Typeflag: tar.TypeReg, Mode: 0o644}),
	)
	output := filepath.Join(t.TempDir(), "dist", "linux-amd64")

	var log strings.Builder

	err := layoutExecutor(t, fixture).Build(t.Context(), container.BuildRequest{Context: ".", Target: "export-binary", Mode: container.BuildModeLocal, OutputDir: output}, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}

	if body, err := os.ReadFile(filepath.Join(output, "hello")); err != nil || string(body) != "content of hello" {
		t.Fatalf("exported binary: %q %v", body, err)
	}

	if _, err := os.Lstat(filepath.Join(output, "stale")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a whited-out file was exported: %v", err)
	}
}

// Image layers are untrusted: a hostile member stops the export before
// anything reaches the output directory. An escaping symlink never reaches
// the extractor: go-containerregistry's flattening already drops it, so the
// export succeeds without it (internal/archive refuses one on its own; see
// its tests).
func TestBuild_LocalModeRefusesHostileLayers(t *testing.T) {
	t.Parallel()

	for name, hostile := range map[string]tar.Header{
		"parent traversal": {Name: "../escape", Typeflag: tar.TypeReg, Mode: 0o644},
		"absolute path":    {Name: "/escape", Typeflag: tar.TypeReg, Mode: 0o644},
		"symlink escape":   {Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../../escape"},
		"device":           {Name: "null", Typeflag: tar.TypeChar, Devmajor: 1, Devminor: 3},
		"hardlink":         {Name: "again", Typeflag: tar.TypeLink, Linkname: "hello"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := layoutFixture(t, layerOf(t, tar.Header{Name: "hello", Typeflag: tar.TypeReg, Mode: 0o755}, hostile))
			parent := t.TempDir()
			output := filepath.Join(parent, "dist")

			err := layoutExecutor(t, fixture).Build(t.Context(), container.BuildRequest{Context: ".", Mode: container.BuildModeLocal, OutputDir: output}, &strings.Builder{})

			leaks := []string{filepath.Join(parent, "escape"), "/escape", filepath.Join(output, hostile.Name)}

			if name == "symlink escape" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil {
					t.Fatal("a hostile layer was exported")
				}

				leaks = append(leaks, filepath.Join(output, "hello"))
			}

			for _, leaked := range leaks {
				if _, statErr := os.Lstat(leaked); !errors.Is(statErr, os.ErrNotExist) {
					t.Errorf("%s exists after a refused export: %v", leaked, statErr)
				}
			}
		})
	}
}

// A build ends in the class of the error kaniko ended on: a registry it
// could not reach is an unavailable dependency, and a RUN step that could
// not reach a host is the build's failure.
func TestBuildToLayout_ClassifiesTheErrorKanikoEndsOn(t *testing.T) {
	t.Parallel()

	const unreached = `error building image: unable to complete operation after 0 attempts, last error: Get "https://index.docker.io/v2/": dial tcp: lookup index.docker.io: i/o timeout`

	for said, want := range map[string]error{
		unreached: errs.ErrDependencyUnavailable,
		`error building image: error building stage: failed to execute command: waiting for process to exit: exit status 1`: errs.ErrValidation,
	} {
		bins := mockbinary.New(t)
		bins.Add("executor", `echo 'dial tcp: lookup mirror.example: i/o timeout' >&2; printf '%s\n' '`+said+`'; exit 1`)
		adapter := &Adapter{Executor: bins.Path("executor"), euid: func() int { return 0 }}

		var log strings.Builder

		err := adapter.BuildToLayout(t.Context(), container.BuildRequest{Context: ".", Mode: container.BuildModeOCI, OutputDir: t.TempDir()}, t.TempDir(), &log)
		if err == nil || !errors.Is(err, want) || errs.ExitCodeFromError(err) != errs.ExitCodeFromError(want) {
			t.Errorf("%s: err = %v, want %v", said, err, want)
		}

		if !strings.Contains(log.String(), said) {
			t.Errorf("the build log lost output: %q", log.String())
		}
	}
}
