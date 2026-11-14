// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRoutes(t *testing.T) {
	for path, want := range map[string]string{"/": "Hello from my-service!\n", "/health": "ok\n"} {
		recorder := httptest.NewRecorder()
		routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

		if recorder.Code != http.StatusOK || recorder.Body.String() != want {
			t.Errorf("GET %s = %d %q, want 200 %q", path, recorder.Code, recorder.Body.String(), want)
		}
	}
}
