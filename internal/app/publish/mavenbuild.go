// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/build"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domainpublish "github.com/diggsweden/forge-ci/internal/domain/publish"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// StageMavenBuild converts the downloaded library build handoff to the existing
// Maven repository publication contract. It executes no project or tool code:
// POMs and main/source/Javadoc bytes are copied, never rebuilt. The caller owns
// the empty temporary destination and validates/uploads it through the ordinary
// stable or snapshot publisher only after this complete preflight succeeds.
func StageMavenBuild(ctx context.Context, directory, destination, version string, snapshot bool) error {
	source, err := pathsafe.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	target, err := pathsafe.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer func() { _ = target.Close() }()
	stage := mavenBuildStage{source: source, target: target, version: strings.TrimPrefix(version, "v"), snapshot: snapshot, seen: map[string]bool{}}
	if err := stage.module(ctx, "."); err != nil {
		return err
	}
	var set MavenPublicationSet
	if snapshot {
		set, err = InspectMavenSnapshot(destination, "")
	} else {
		set, err = InspectMavenPublication(destination, strings.TrimPrefix(version, "v"), false)
	}
	if err != nil {
		return err
	}
	for _, file := range set.Payloads {
		if err := writeMavenChecksums(target, file); err != nil {
			return err
		}
	}
	return nil
}

type mavenBuildStage struct {
	source, target *os.Root
	version        string
	snapshot       bool
	seen           map[string]bool
	bytes          int64
}

func (stage *mavenBuildStage) module(ctx context.Context, module string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !fs.ValidPath(module) || stage.seen[module] || len(stage.seen) >= 128 {
		return fmt.Errorf("Maven build handoff has an invalid, repeated or excessive reactor module: %w", errs.ErrValidation)
	}
	stage.seen[module] = true
	pom := path.Join(module, "pom.xml")
	info, err := stage.source.Lstat(pom)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 1<<20 {
		return fmt.Errorf("build POM must be a bounded nonempty regular file: %w", errs.ErrValidation)
	}
	body, err := stage.source.ReadFile(pom)
	if err != nil {
		return err
	}
	// Reuse the complete Maven XML-envelope validation before retaining the raw
	// POM bytes; xml.Unmarshal alone would ignore a second document root.
	if _, err := build.ParsePOM(body); err != nil {
		return err
	}
	var publication domainpublish.MavenPublication
	if stage.snapshot {
		publication, err = domainpublish.ParseMavenSnapshot(body, "", false)
	} else {
		publication, err = domainpublish.ParseMavenPublication(body, stage.version, false)
	}
	if err != nil {
		return fmt.Errorf("build handoff %s: %w", pom, err)
	}
	for _, profile := range publication.Profiles {
		if len(profile.Modules) != 0 {
			return fmt.Errorf("prebuilt Maven handoff requires an explicit reactor module list, not profile-selected modules: %w", errs.ErrUnsupported)
		}
	}
	if err := stage.file(pom, path.Join(publication.Directory(), publication.Stem()+".pom")); err != nil {
		return err
	}
	if publication.Packaging != "pom" {
		if publication.Packaging != "jar" {
			return fmt.Errorf("prebuilt Maven library handoff supports jar or pom packaging: %w", errs.ErrUnsupported)
		}
		name := publication.Build.FinalName
		if name == "" {
			name = publication.Stem()
		}
		name = strings.NewReplacer("${project.artifactId}", publication.ArtifactID, "${project.version}", publication.Version).Replace(name)
		if !fs.ValidPath(name) || strings.ContainsAny(name, "/\\${}") {
			return fmt.Errorf("build finalName must resolve to a plain basename: %w", errs.ErrUnsupported)
		}
		for _, suffix := range []string{".jar", "-sources.jar", "-javadoc.jar"} {
			if err := stage.file(path.Join(module, "target", name+suffix), path.Join(publication.Directory(), publication.Stem()+suffix)); err != nil {
				return fmt.Errorf("prebuilt library requires main/source/Javadoc from its build job: %w", err)
			}
		}
		entries, err := fs.ReadDir(stage.source.FS(), path.Join(module, "target"))
		if err != nil {
			return err
		}
		for _, entry := range entries {
			suffix, classified := strings.CutPrefix(entry.Name(), name+"-")
			if !classified || !strings.HasSuffix(suffix, ".jar") || suffix == "sources.jar" || suffix == "javadoc.jar" {
				continue
			}
			if err := stage.file(path.Join(module, "target", entry.Name()), path.Join(publication.Directory(), publication.Stem()+"-"+suffix)); err != nil {
				return err
			}
		}
	}
	for _, child := range publication.Modules {
		if err := stage.module(ctx, path.Join(module, child)); err != nil {
			return err
		}
	}
	return nil
}

func (stage *mavenBuildStage) file(from, to string) error {
	info, err := stage.source.Lstat(from)
	if err != nil {
		return err
	}
	if err := validatePublicationFile(info); err != nil {
		return err
	}
	stage.bytes += info.Size()
	if stage.bytes > 2<<30 {
		return fmt.Errorf("Maven build publication exceeds 2 GiB: %w", errs.ErrValidation)
	}
	input, err := stage.source.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	if err := stage.target.MkdirAll(path.Dir(to), 0o700); err != nil {
		return err
	}
	output, err := stage.target.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.CopyN(output, input, info.Size())
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
