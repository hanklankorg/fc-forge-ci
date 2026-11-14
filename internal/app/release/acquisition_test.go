// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	githubadapter "github.com/diggsweden/forge-ci/internal/adapters/github"
	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	appsbom "github.com/diggsweden/forge-ci/internal/app/sbom"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeprovider"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
	"github.com/stretchr/testify/require"
)

const acquisitionPlan = `{"version":1,"items":[{"kind":"build_artifact","name":"build-artifacts","path":"release-artifacts","required":true}]}`

type acquisitionTransport func(*http.Request) (*http.Response, error)

func (transport acquisitionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return transport(req)
}

// Use the real native GitHub ZIP extraction/staging path, but never a socket,
// token, provider service, or process. The ZIP is the independent producer input.
func plannedZIPDownloader(t *testing.T, members map[string]string) provider.RunArtifactDownloader {
	t.Helper()

	var archive bytes.Buffer

	writer := zip.NewWriter(&archive)
	for name, body := range members {
		file, err := writer.Create(name)
		require.NoError(t, err)
		_, err = io.WriteString(file, body)
		require.NoError(t, err)
	}

	require.NoError(t, writer.Close())

	return &githubadapter.Provider{
		APIBaseOverride: "https://github.invalid",
		Env:             func(string) string { return "" },
		HTTPClient: &http.Client{Transport: acquisitionTransport(func(req *http.Request) (*http.Response, error) {
			response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: req}
			switch req.URL.Path {
			case "/repos/o/r/actions/runs/1/artifacts":
				response.Body = io.NopCloser(strings.NewReader(`{"total_count":1,"artifacts":[{"id":7,"name":"build-artifacts"}]}`))
			case "/repos/o/r/actions/artifacts/7/zip":
				response.StatusCode = http.StatusFound
				response.Header.Set("Location", "https://blob.invalid/planned.zip")
				response.Body = io.NopCloser(strings.NewReader(""))
			case "/planned.zip":
				response.Body = io.NopCloser(bytes.NewReader(archive.Bytes()))
			default:
				t.Fatalf("unexpected offline request %s", req.URL)
			}

			return response, nil
		})},
	}
}

func TestAcquisition_SourceFilesCannotBecomeProducerAssets(t *testing.T) {
	fsys := testfs.NewReal(t)
	fsys.Chdir()
	t.Setenv("CI_TEMP_DIR", t.TempDir())
	fsys.WriteFile("release-artifacts/unexpected.jar", []byte("source preseed"))
	fsys.WriteFile("services/api/fixture-sbom.spdx.json", []byte(`{"source":"fixture"}`))
	fsys.WriteFile("root-sbom.cyclonedx.json", []byte(`{"source":"root fixture"}`))
	fsys.WriteFile("explicit.txt", []byte("explicit attachment"))

	dir, err := apprelease.AcquireArtifacts(t.Context(), plannedZIPDownloader(t, map[string]string{"expected.jar": "actual producer\x00bytes"}), nil, nil,
		apprelease.DownloadArtifactsInput{ArtifactTransferPlanJSON: acquisitionPlan, RunID: "1", Repository: "o/r"})
	require.NoError(t, err)
	asm, err := apprelease.Assemble(nil, apprelease.AssembleInput{
		ConfigPlanJSON: releaseAssemblyConfigJSON(t), ArtifactTransferPlanJSON: acquisitionPlan,
		AcquisitionDir: dir, ProjectName: "my-app", Version: "v1.2.3", AttachArtifacts: "explicit.txt",
	})
	require.NoError(t, err)
	require.Len(t, asm.Assets, 2)
	require.Empty(t, asm.SBOMs, "neither source-root nor configured-working-directory fixtures are producer evidence")
	require.Equal(t, "expected.jar", asm.Assets[0].Name)
	require.Equal(t, "build_artifact", asm.Assets[0].SourceKind)
	require.Equal(t, "build-artifacts", asm.Assets[0].SourceArtifact)
	require.Equal(t, "attachment", asm.Assets[1].SourceKind)
	require.Empty(t, asm.Assets[1].SourceArtifact)
	require.Equal(t, "actual producer\x00bytes", readFile(t, asm.Assets[0].Path))

	_, err = apprelease.Checksums(io.Discard, apprelease.ChecksumsInput{AssemblyFile: domainrelease.DefaultAssemblyFile})
	require.NoError(t, err)
	require.Equal(t, []string{"expected.jar", "explicit.txt"}, checksumSubjects(t, asm.ChecksumFile))

	signer := &fakeSigner{}
	require.NoError(t, apprelease.SignArtifacts(t.Context(), signer, io.Discard, apprelease.SignInput{AssemblyFile: domainrelease.DefaultAssemblyFile}))
	require.Equal(t, []string{"release-files/assets/expected.jar", "release-files/assets/explicit.txt", asm.ChecksumFile}, signer.signed)
	prov := fakeprovider.New(t)
	require.NoError(t, apprelease.CreateRelease(t.Context(), prov, &fakeFS{Files: map[string]bool{}}, io.Discard, apprelease.CreateReleaseInput{
		Tag: "v1.2.3", Repository: "o/r", AssemblyFile: domainrelease.DefaultAssemblyFile,
	}))

	for _, path := range prov.CreateReleaseCalls()[0].Spec.Assets {
		require.False(t, strings.Contains(path, "unexpected") || strings.Contains(path, "fixture") || strings.Contains(path, "sbom"))
	}

	require.Equal(t, "source preseed", readFile(t, "release-artifacts/unexpected.jar"))
}

func TestAcquisition_IndependentInventoryRejectsActualProducerExtraOrMissing(t *testing.T) {
	for name, members := range map[string]map[string]string{
		"complete": {"expected.jar": "producer"},
		"extra":    {"expected.jar": "producer", "extra.jar": "also actually uploaded"},
		"missing":  {"other.jar": "not the expected member"},
	} {
		t.Run(name, func(t *testing.T) {
			fsys := testfs.NewReal(t)
			fsys.Chdir()
			t.Setenv("CI_TEMP_DIR", t.TempDir())
			fsys.WriteFile("oracle/expected.jar", []byte("producer"))

			dir, err := apprelease.AcquireArtifacts(t.Context(), plannedZIPDownloader(t, members), nil, nil,
				apprelease.DownloadArtifactsInput{ArtifactTransferPlanJSON: acquisitionPlan, RunID: "1", Repository: "o/r"})
			require.NoError(t, err)
			asm, err := apprelease.Assemble(nil, apprelease.AssembleInput{
				ConfigPlanJSON: releaseAssemblyConfigJSON(t), ArtifactTransferPlanJSON: acquisitionPlan,
				AcquisitionDir: dir, ProjectName: "app", Version: "v1.2.3",
			})
			require.NoError(t, err)
			// Assembly must preserve actual producer members, not hide extras to
			// make an expected inventory pass. This oracle is NOT assembly-derived.
			require.Len(t, asm.Assets, len(members))

			err = apprelease.CompareReleaseFiles("oracle", "release-files/assets", []string{"expected.jar"}, []string{"*.jar"})
			if name == "complete" {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, errs.ErrValidation)
			}
		})
	}
}

func TestAcquisition_PreservesProducerSBOMMembers(t *testing.T) {
	fsys := testfs.NewReal(t)
	fsys.Chdir()
	t.Setenv("CI_TEMP_DIR", t.TempDir())
	fsys.WriteFile("same-sbom.spdx.json", []byte("source fixture with the same basename"))

	const producerSBOM = `{"spdxVersion":"SPDX-2.3","name":"actual producer"}`

	dir, err := apprelease.AcquireArtifacts(t.Context(), plannedZIPDownloader(t, map[string]string{
		"expected.jar": "producer", "same-sbom.spdx.json": producerSBOM,
	}), nil, nil, apprelease.DownloadArtifactsInput{ArtifactTransferPlanJSON: acquisitionPlan, RunID: "1", Repository: "o/r"})
	require.NoError(t, err)
	asm, err := apprelease.Assemble(nil, apprelease.AssembleInput{
		ConfigPlanJSON: releaseAssemblyConfigJSON(t), ArtifactTransferPlanJSON: acquisitionPlan,
		AcquisitionDir: dir, ProjectName: "app", Version: "v1.2.3",
	})
	require.NoError(t, err)
	require.Len(t, asm.SBOMs, 1)
	require.Equal(t, sha256.Sum256([]byte(producerSBOM)), sha256.Sum256([]byte(readFile(t, asm.SBOMs[0].Path))))
	require.Equal(t, sha256.Sum256([]byte(producerSBOM)), sha256.Sum256([]byte(readFile(t, filepath.Join(dir, "release-artifacts/same-sbom.spdx.json")))))
	require.Equal(t, "source fixture with the same basename", readFile(t, "same-sbom.spdx.json"))
}

func TestAcquisition_ExplicitSourceSBOMRemainsAnAttachment(t *testing.T) {
	fsys := testfs.NewReal(t)
	fsys.Chdir()
	t.Setenv("CI_TEMP_DIR", t.TempDir())
	fsys.WriteFile("explicit-sbom.spdx.json", []byte("explicit source SBOM"))

	dir, err := apprelease.AcquireArtifacts(t.Context(), plannedZIPDownloader(t, map[string]string{"expected.jar": "producer"}), nil, nil,
		apprelease.DownloadArtifactsInput{ArtifactTransferPlanJSON: acquisitionPlan, RunID: "1", Repository: "o/r"})
	require.NoError(t, err)
	asm, err := apprelease.Assemble(nil, apprelease.AssembleInput{
		ConfigPlanJSON: releaseAssemblyConfigJSON(t), ArtifactTransferPlanJSON: acquisitionPlan,
		AcquisitionDir: dir, ProjectName: "app", Version: "v1.2.3", AttachArtifacts: "explicit-sbom.spdx.json",
	})
	require.NoError(t, err)
	require.Len(t, asm.Assets, 2)
	require.Empty(t, asm.SBOMs)
	require.Equal(t, "attachment", asm.Assets[1].SourceKind)
	require.Empty(t, asm.Assets[1].SourceArtifact)
	require.Equal(t, "explicit source SBOM", readFile(t, asm.Assets[1].Path))
}

func TestAcquireArtifacts_RefusalAndFailureAreBounded(t *testing.T) {
	fsys := testfs.NewReal(t)
	fsys.Chdir()

	temp := t.TempDir()
	t.Setenv("CI_TEMP_DIR", temp)
	require.NoError(t, os.WriteFile(filepath.Join(temp, "caller-canary"), []byte("keep"), 0o600))

	dl := &fakeArtifactDownloader{}
	_, err := apprelease.AcquireArtifacts(t.Context(), dl, nil, nil, apprelease.DownloadArtifactsInput{
		ArtifactTransferPlanJSON: strings.Replace(acquisitionPlan, `"required":true`, `"required":true},{"kind":"unknown","name":"bad","path":"x"`, 1),
	})
	require.ErrorIs(t, err, errs.ErrInvalidConfig)
	require.Empty(t, dl.calls)
	dl.fail = map[string]error{"build-artifacts": errArtifactNotFound}
	_, err = apprelease.AcquireArtifacts(context.Background(), dl, nil, nil, apprelease.DownloadArtifactsInput{ArtifactTransferPlanJSON: acquisitionPlan})
	require.ErrorIs(t, err, errArtifactNotFound)
	entries, err := os.ReadDir(temp)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "caller-canary", entries[0].Name())

	// Retry acquires another fresh root, not the previously failed destination.
	failedDir := dl.calls[0].Dir
	dir, err := apprelease.AcquireArtifacts(t.Context(), plannedZIPDownloader(t, map[string]string{"expected.jar": "producer"}), nil, nil,
		apprelease.DownloadArtifactsInput{ArtifactTransferPlanJSON: acquisitionPlan, RunID: "1", Repository: "o/r"})
	require.NoError(t, err)
	require.NotEqual(t, failedDir, filepath.Join(dir, "release-artifacts"))
	_, err = os.Stat(failedDir)
	require.ErrorIs(t, err, os.ErrNotExist)

	t.Setenv("CI_TEMP_DIR", fsys.Root)
	_, err = apprelease.AcquireArtifacts(t.Context(), dl, nil, nil, apprelease.DownloadArtifactsInput{ArtifactTransferPlanJSON: acquisitionPlan})
	require.ErrorIs(t, err, errs.ErrValidation)
}

type acquisitionScanner struct{ calls int }

func (scanner *acquisitionScanner) Generate(context.Context, string, map[string]string, io.Writer) error {
	scanner.calls++

	return nil
}

func acquisitionTreeSnapshot(t *testing.T, root string) map[string][sha256.Size]byte {
	t.Helper()

	snapshot := make(map[string][sha256.Size]byte)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		body := []byte(entry.Type().String())
		if entry.Type().IsRegular() {
			var err error

			body, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}

		snapshot[path] = sha256.Sum256(body)

		return nil
	})
	require.NoError(t, err)

	return snapshot
}

func TestAcquisition_AssemblyAndSBOMRefuseOverlapsBeforeEffects(t *testing.T) {
	for _, location := range []string{"source", ".", "source/acquired", "alias/source", "alias", "alias/source/acquired"} {
		t.Run(location, func(t *testing.T) {
			fsys := testfs.NewReal(t)
			fsys.WriteFile("source/release-artifacts/api", []byte("source binary"))
			fsys.WriteFile("source/fixture-sbom.spdx.json", []byte("source SBOM"))
			fsys.WriteFile("source/release-files/assets/canary", []byte("existing staged output"))
			fsys.WriteFile("source/.forge-ci/release-assembly.json", []byte("existing assembly"))
			fsys.MkdirAll("source/acquired")
			require.NoError(t, os.Symlink(fsys.Root, fsys.Path("alias")))
			t.Chdir(fsys.Path("source"))
			before := acquisitionTreeSnapshot(t, fsys.Root)

			_, err := apprelease.Assemble(nil, apprelease.AssembleInput{
				ConfigPlanJSON: releaseAssemblyConfigJSON(t), ArtifactTransferPlanJSON: acquisitionPlan,
				AcquisitionDir: fsys.Path(location), ProjectName: "api", Version: "v1.2.3",
			})
			require.ErrorIs(t, err, errs.ErrValidation)
			require.Equal(t, before, acquisitionTreeSnapshot(t, fsys.Root))

			for _, layers := range []string{"analyzed-artifact", "none"} {
				scanner := &acquisitionScanner{}
				git := newFakeGitOps()
				err = appsbom.GenerateArtifacts(t.Context(), scanner, nil, git, io.Discard, io.Discard, appsbom.GenerateArtifactsInput{
					ConfigPlanJSON: releaseAssemblyConfigJSON(t), SBOMs: layers, Version: "1.2.3", AcquisitionDir: fsys.Path(location),
				})
				require.ErrorIs(t, err, errs.ErrValidation)
				require.Zero(t, scanner.calls)
				require.Empty(t, git.other)
				require.Equal(t, before, acquisitionTreeSnapshot(t, fsys.Root))
			}
		})
	}
}

func TestAcquireArtifacts_TempParentMayContainCheckout(t *testing.T) {
	fsys := testfs.NewReal(t)
	fsys.WriteFile("source/canary", []byte("source"))
	t.Chdir(fsys.Path("source"))
	t.Setenv("CI_TEMP_DIR", fsys.Root)
	dir, err := apprelease.AcquireArtifacts(t.Context(), plannedZIPDownloader(t, map[string]string{"expected.jar": "producer"}), nil, nil,
		apprelease.DownloadArtifactsInput{ArtifactTransferPlanJSON: acquisitionPlan, RunID: "1", Repository: "o/r"})
	require.NoError(t, err)
	require.Equal(t, fsys.Root, filepath.Dir(dir))
	require.Equal(t, "source", readFile(t, "canary"))
}

type acquisitionSequenceDownloader struct {
	downloaders []provider.RunArtifactDownloader
	calls       []provider.RunArtifactDownload
}

func (downloader *acquisitionSequenceDownloader) DownloadRunArtifact(ctx context.Context, request provider.RunArtifactDownload) (provider.RunArtifactInfo, error) {
	index := len(downloader.calls)
	downloader.calls = append(downloader.calls, request)

	return downloader.downloaders[index].DownloadRunArtifact(ctx, request)
}

type acquisitionCountOnlyDownloader struct{ symlink bool }

func (downloader acquisitionCountOnlyDownloader) DownloadRunArtifact(_ context.Context, request provider.RunArtifactDownload) (provider.RunArtifactInfo, error) {
	if downloader.symlink {
		if err := os.Symlink("absent", filepath.Join(request.Dir, "not-regular")); err != nil {
			return provider.RunArtifactInfo{}, err
		}
	}

	return provider.RunArtifactInfo{Name: request.Name, FileCount: 99}, nil
}

func TestAcquireArtifacts_RequiredTransfersNeedActualRegularMembers(t *testing.T) {
	for _, kind := range []string{"empty ZIP", "directory-only ZIP", "count without files", "count with symlink", "later empty transfer"} {
		t.Run(kind, func(t *testing.T) {
			fsys := testfs.NewReal(t)
			fsys.Chdir()

			temp := testfs.NewReal(t)
			temp.WriteFile("caller-canary", []byte("untouched"))
			t.Setenv("CI_TEMP_DIR", temp.Root)
			before := acquisitionTreeSnapshot(t, temp.Root)
			sink := fakeoutputsink.New(t)
			valid := plannedZIPDownloader(t, map[string]string{"expected.jar": "actual producer"})
			bad := plannedZIPDownloader(t, nil)
			plan := acquisitionPlan

			if kind == "directory-only ZIP" {
				bad = plannedZIPDownloader(t, map[string]string{"only/": "", "only/nested/": ""})
			}

			if kind == "count without files" {
				bad = acquisitionCountOnlyDownloader{}
			}

			if kind == "count with symlink" {
				bad = acquisitionCountOnlyDownloader{symlink: true}
			}

			downloads := []provider.RunArtifactDownloader{bad}

			if kind == "later empty transfer" {
				// Both transfers intentionally target the same destination. The
				// first one's real file cannot satisfy the second one's obligation.
				plan = strings.Replace(acquisitionPlan, `}]}`, `},{"kind":"build_artifact","name":"build-artifacts","path":"release-artifacts","required":true}]}`, 1)
				downloads = []provider.RunArtifactDownloader{valid, bad}
			}

			downloader := &acquisitionSequenceDownloader{downloaders: downloads}
			input := apprelease.DownloadArtifactsInput{ArtifactTransferPlanJSON: plan, RunID: "1", Repository: "o/r"}

			dir, err := apprelease.AcquireArtifacts(t.Context(), downloader, sink, nil, input)
			if kind == "count with symlink" {
				require.ErrorIs(t, err, errs.ErrValidation)
			} else {
				require.ErrorIs(t, err, errs.ErrMissingInput)
			}

			require.Empty(t, dir)
			require.Empty(t, sink.AllScalar(), "no acquisition output, even after an earlier transfer completed")
			require.Equal(t, before, acquisitionTreeSnapshot(t, temp.Root), "cleanup owns only the new private root")

			failedPaths := make(map[string]bool)
			for _, call := range downloader.calls {
				failedPaths[call.Dir] = true
			}

			// Retry is a new acquisition, never a cached partial namespace.
			retry := &acquisitionSequenceDownloader{downloaders: []provider.RunArtifactDownloader{valid, valid}}
			dir, err = apprelease.AcquireArtifacts(t.Context(), retry, sink, nil, input)
			require.NoError(t, err)
			require.Equal(t, dir, sink.Single("acquisition-dir"))
			require.Equal(t, "actual producer", readFile(t, filepath.Join(dir, "release-artifacts/expected.jar")))

			for _, call := range retry.calls {
				require.NotContains(t, failedPaths, call.Dir)
			}
		})
	}
}

func TestDownloadArtifacts_LegacyEmptyZIPRemainsAccepted(t *testing.T) {
	fsys := testfs.NewReal(t)
	fsys.Chdir()

	err := apprelease.DownloadArtifacts(t.Context(), plannedZIPDownloader(t, nil), nil,
		apprelease.DownloadArtifactsInput{ArtifactTransferPlanJSON: acquisitionPlan, RunID: "1", Repository: "o/r"})
	require.NoError(t, err)
}
