// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package conformance_test

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	appconfig "github.com/diggsweden/forge-ci/internal/app/config"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// releaseFilesDir is where a release's build leaves the files it hands over,
// and so where a Containerfile finds one of them in its build context.
const releaseFilesDir = "dist/"

// releaseFileCopy is one file of a release that an image carries: the build
// context holds it at Source, the release offers it under Asset, and the
// image holds it at Path.
type releaseFileCopy struct {
	Source, Asset, Path string
}

// releaseFileCopies reads from a Containerfile which files of the release's
// hand-off the image of one architecture copies in, and where to. A COPY
// from another stage is the image's own build and names no release file. A
// hand-off COPY the reader cannot resolve to one file and one absolute path
// is a problem, never a silent omission.
func releaseFileCopies(containerfile, arch string) ([]releaseFileCopy, []string) {
	var (
		copies   []releaseFileCopy
		problems []string
	)

	resolve := strings.NewReplacer("${TARGETARCH}", arch, "$TARGETARCH", arch)

	for line := range strings.SplitSeq(containerfile, "\n") {
		words := strings.Fields(line)
		if len(words) == 0 || !strings.EqualFold(words[0], "COPY") {
			continue
		}

		args := slices.DeleteFunc(words[1:], func(word string) bool { return strings.HasPrefix(word, "--") })
		if len(args) < 2 || slices.ContainsFunc(words[1:], func(word string) bool { return strings.HasPrefix(word, "--from=") }) {
			continue
		}

		target := resolve.Replace(args[len(args)-1])

		for _, written := range args[:len(args)-1] {
			source := resolve.Replace(strings.TrimPrefix(written, "./"))
			// A directory is no file of the release: an image built from an
			// unpacked package copies the package's own dist/.
			if !strings.HasPrefix(source, releaseFilesDir) || strings.HasSuffix(source, "/") {
				continue
			}

			to := target
			if strings.HasSuffix(to, "/") {
				to += path.Base(source)
			}

			if strings.ContainsAny(source+to, "$*?[") || !path.IsAbs(to) || len(args) > 2 && !strings.HasSuffix(target, "/") {
				problems = append(problems, fmt.Sprintf("`%s` copies from the release's files, and names no one file and absolute path to compare", strings.TrimSpace(line)))

				continue
			}

			copies = append(copies, releaseFileCopy{Source: source, Asset: path.Base(source), Path: path.Clean(to)})
		}
	}

	return copies, problems
}

// plannedReleaseFileCopies is releaseFileCopies of every planned image's
// build for one architecture, by image name, each Source a path of the
// checkout the build context lies in. read returns a file of the source the
// plan was made from.
func plannedReleaseFileCopies(plan pipeline.ReleaseImagesPlan, arch string, read func(file string) (string, error)) (map[string][]releaseFileCopy, []string) {
	copies, problems := map[string][]releaseFileCopy{}, []string{}

	for _, build := range plan.Builds {
		if build.Arch != arch {
			continue
		}

		containerfile, err := read(build.ContainerFile)
		if err != nil {
			problems = append(problems, fmt.Sprintf("image %s: read %s: %v", build.Name, build.ContainerFile, err))

			continue
		}

		found, unresolved := releaseFileCopies(containerfile, arch)
		for _, problem := range unresolved {
			problems = append(problems, fmt.Sprintf("image %s, %s: %s", build.Name, build.ContainerFile, problem))
		}

		for index := range found {
			found[index].Source = path.Join(build.Context, found[index].Source)
		}

		if len(found) > 0 {
			copies[build.Name] = found
		}
	}

	return copies, problems
}

// imageReleaseFileFindings holds one image to the release files it copies:
// each is a file the release offers, a job of the run handed the image build
// that file at the path the Containerfile copies, and the image's file has
// its bytes. released is the SHA-256 of each release file by name, handedOver
// those of the files the run's artifacts hold at a checkout path, and inImage
// that of each image file by path without the leading slash.
func imageReleaseFileFindings(image string, copies []releaseFileCopy, released map[string]string, handedOver map[string][]string, inImage map[string]string) []string {
	var findings []string

	for _, file := range copies {
		want, offered := released[file.Asset]
		got, held := inImage[strings.TrimPrefix(file.Path, "/")]

		switch {
		case !offered || want == "":
			findings = append(findings, fmt.Sprintf("image %s copies %s to %s, and the release offers no file of that name", image, file.Asset, file.Path))
		case !slices.Contains(handedOver[file.Source], want):
			findings = append(findings, fmt.Sprintf("image %s copies %s, and no artifact of the run holds the release's %s there: the image build was not handed the released file", image, file.Source, file.Asset))
		case !held || got == "":
			findings = append(findings, fmt.Sprintf("image %s holds no file %s to compare with the release's %s", image, file.Path, file.Asset))
		case got != want:
			findings = append(findings, fmt.Sprintf("image %s holds %s with sha256 %s, and the release's %s is %s: the image was not built from the released file", image, file.Path, got, file.Asset, want))
		}
	}

	return findings
}

func TestReleaseFileCopies(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		containerfile string
		want          []releaseFileCopy
		problems      int
	}{
		"one file by architecture": {
			containerfile: "FROM scratch\nARG TARGETARCH\nCOPY dist/linux-${TARGETARCH}/app-linux-${TARGETARCH} /usr/local/bin/app\n",
			want:          []releaseFileCopy{{Source: "dist/linux-amd64/app-linux-amd64", Asset: "app-linux-amd64", Path: "/usr/local/bin/app"}},
		},
		"into a directory, with flags and lower case": {
			containerfile: "copy --chmod=0755 ./dist/linux-$TARGETARCH/app-linux-$TARGETARCH /opt/\n",
			want:          []releaseFileCopy{{Source: "dist/linux-amd64/app-linux-amd64", Asset: "app-linux-amd64", Path: "/opt/app-linux-amd64"}},
		},
		"a stage's file is the image's own build": {
			containerfile: "FROM rust AS builder\nRUN cargo build\nFROM scratch\nCOPY --from=builder /src/target/release/app /usr/local/bin/app\n",
		},
		"a source outside the hand-off": {
			containerfile: "COPY Cargo.toml Cargo.lock ./\nCOPY src ./src\n",
		},
		"a directory is a package's own, not a file of the release": {
			containerfile: "COPY --chmod=0755 dist/ /app/\n",
		},
		"a glob names no one file": {
			containerfile: "COPY dist/linux-amd64/* /usr/local/bin/\n",
			problems:      1,
		},
		"an unknown argument": {
			containerfile: "COPY dist/${FLAVOR}/app /usr/local/bin/app\n",
			problems:      1,
		},
		"a relative destination": {
			containerfile: "WORKDIR /opt\nCOPY dist/linux-amd64/app app\n",
			problems:      1,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, problems := releaseFileCopies(test.containerfile, "amd64")
			if !slices.Equal(got, test.want) || len(problems) != test.problems {
				t.Fatalf("copies %+v with problems %q, want %+v with %d problems", got, problems, test.want, test.problems)
			}
		})
	}
}

func TestImageReleaseFileFindings(t *testing.T) {
	t.Parallel()

	const same, other = "aa11", "bb22"

	const source = "dist/linux-amd64/app-linux-amd64"

	copies := []releaseFileCopy{{Source: source, Asset: "app-linux-amd64", Path: "/usr/local/bin/app"}}

	for name, test := range map[string]struct {
		released, inImage map[string]string
		handedOver        map[string][]string
		want              string
	}{
		"the released bytes": {
			released: map[string]string{"app-linux-amd64": same}, handedOver: map[string][]string{source: {other, same}},
			inImage: map[string]string{"usr/local/bin/app": same},
		},
		"other bytes in the image": {
			released: map[string]string{"app-linux-amd64": same}, handedOver: map[string][]string{source: {same}},
			inImage: map[string]string{"usr/local/bin/app": other},
			want:    "was not built from the released file",
		},
		"not released": {
			released: map[string]string{"app-linux-arm64": same}, handedOver: map[string][]string{source: {same}},
			inImage: map[string]string{"usr/local/bin/app": same},
			want:    "the release offers no file of that name",
		},
		"not handed to the image build": {
			released: map[string]string{"app-linux-amd64": same}, handedOver: map[string][]string{"linux-amd64/app-linux-amd64": {same}},
			inImage: map[string]string{"usr/local/bin/app": same},
			want:    "the image build was not handed the released file",
		},
		"other bytes handed over": {
			released: map[string]string{"app-linux-amd64": same}, handedOver: map[string][]string{source: {other}},
			inImage: map[string]string{"usr/local/bin/app": same},
			want:    "the image build was not handed the released file",
		},
		"not in the image": {
			released: map[string]string{"app-linux-amd64": same}, handedOver: map[string][]string{source: {same}},
			inImage: map[string]string{"usr/bin/app": same},
			want:    "holds no file /usr/local/bin/app",
		},
		"an empty digest is no match": {
			released: map[string]string{"app-linux-amd64": ""}, handedOver: map[string][]string{source: {""}},
			inImage: map[string]string{"usr/local/bin/app": ""},
			want:    "the release offers no file of that name",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			findings := imageReleaseFileFindings("app", copies, test.released, test.handedOver, test.inImage)
			if test.want == "" && len(findings) != 0 || test.want != "" && (len(findings) != 1 || !strings.Contains(findings[0], test.want)) {
				t.Fatalf("findings %q, want one holding %q", findings, test.want)
			}
		})
	}
}

// The release lines that read their images back compare bytes only where the
// planned Containerfile copies a release file. The Go service's image is
// built from the released binary, so its own declaration and Containerfile
// must keep deriving that comparison: a line that derived none would pass
// having compared nothing. The Cargo workspace's images build for themselves.
func TestArtifactFirstDemosDeriveTheirImageComparison(t *testing.T) {
	t.Parallel()

	for demo, test := range map[string]struct {
		declaration string
		want        map[string][]releaseFileCopy
	}{
		"hello-go-service": {
			declaration: ".forge-ci/images.yml",
			want:        map[string][]releaseFileCopy{"my-service": {{Source: "dist/linux-amd64/my-service-linux-amd64", Asset: "my-service-linux-amd64", Path: "/usr/local/bin/my-service"}}},
		},
		// Container-first: the Containerfile compiles, and nothing is compared.
		"hello-cargo-workspace": {declaration: ".forge-ci/release-images.yml", want: map[string][]releaseFileCopy{}},
	} {
		t.Run(demo, func(t *testing.T) {
			t.Parallel()

			consumer := filepath.Join(reporoot.Path(t), "test", "live", "fixtures", "consumers", demo, "consumer")

			plan, err := appconfig.ResolveReleaseImagesPlan(appconfig.EmitReleaseImagesPlanInput{
				Config: appconfig.EmitConfigPlanInput{Path: filepath.Join(consumer, filepath.FromSlash(test.declaration))},
				Images: pipeline.ReleaseImagesInput{ImageRepository: "registry.invalid/demo", Repository: "demo/demo", ReleaseTag: "v1.2.3", ReleasePlatforms: "linux/amd64"},
			}, output.Annotator{})
			if err != nil {
				t.Fatal(err)
			}

			got, problems := plannedReleaseFileCopies(plan, "amd64", func(file string) (string, error) {
				body, err := os.ReadFile(filepath.Join(consumer, filepath.FromSlash(file)))

				return string(body), err
			})
			if len(problems) != 0 || len(got) != len(test.want) {
				t.Fatalf("copies %+v with problems %q, want %+v", got, problems, test.want)
			}

			for image, want := range test.want {
				if !slices.Equal(got[image], want) {
					t.Errorf("image %s copies %+v, want %+v", image, got[image], want)
				}
			}
		})
	}
}
