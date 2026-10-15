// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"errors"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestBuildRequest_Validate(t *testing.T) {
	t.Parallel()

	base := container.BuildRequest{Context: ".", Mode: container.BuildModeLoad, ImageRef: "img:tag"}

	t.Run("valid load", func(t *testing.T) {
		t.Parallel()

		if err := base.Validate(); err != nil {
			t.Fatalf("valid load request errored: %v", err)
		}
	})

	cases := map[string]container.BuildRequest{
		"empty context":            {Mode: container.BuildModeLoad, ImageRef: "x"},
		"load without image ref":   {Context: ".", Mode: container.BuildModeLoad},
		"push without image ref":   {Context: ".", Mode: container.BuildModePushByDigest},
		"local without output dir": {Context: ".", Mode: container.BuildModeLocal},
		"unknown mode":             {Context: ".", Mode: "bogus", ImageRef: "x"},
		"malformed secret":         {Context: ".", Mode: container.BuildModeLoad, ImageRef: "x", Secrets: []string{"src=/p"}},
	}

	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if err := req.Validate(); !errors.Is(err, errs.ErrUsage) {
				t.Errorf("%s: got %v, want ErrUsage", name, err)
			}
		})
	}

	t.Run("well-formed secret passes", func(t *testing.T) {
		t.Parallel()

		req := base
		req.Secrets = []string{"id=db,src=/run/secrets/db"}

		if err := req.Validate(); err != nil {
			t.Errorf("well-formed secret rejected: %v", err)
		}
	})
}
