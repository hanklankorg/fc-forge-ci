// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

func TestContainerfileRebuilds(t *testing.T) {
	t.Parallel()

	type rebuilds = []container.Rebuild

	tests := []struct {
		name  string
		given string
		want  rebuilds
	}{
		{
			"maven wrapper", "FROM eclipse-temurin:25\nRUN ./mvnw -B clean package -DskipTests",
			rebuilds{{Line: 2, Command: "./mvnw -B clean package -DskipTests", Ecosystem: projecttype.Maven}},
		},
		{"maven verify", "RUN mvn verify", rebuilds{{Line: 1, Command: "mvn verify", Ecosystem: projecttype.Maven}}},
		{
			"gradle wrapper with a project path", "RUN ./gradlew :app:bootJar -x test",
			rebuilds{{Line: 1, Command: "./gradlew :app:bootJar -x test", Ecosystem: projecttype.Gradle}},
		},
		{"gradle", "RUN gradle installDist", rebuilds{{Line: 1, Command: "gradle installDist", Ecosystem: projecttype.Gradle}}},
		{"npm", "RUN npm run build", rebuilds{{Line: 1, Command: "npm run build", Ecosystem: projecttype.NPM}}},
		{"pnpm", "RUN pnpm build", rebuilds{{Line: 1, Command: "pnpm build", Ecosystem: projecttype.NPM}}},
		{"yarn", "RUN yarn run build", rebuilds{{Line: 1, Command: "yarn run build", Ecosystem: projecttype.NPM}}},
		{
			"cargo behind a cache mount", "RUN --mount=type=cache,target=/usr/local/cargo/registry cargo build --release",
			rebuilds{{Line: 1, Command: "cargo build --release", Ecosystem: projecttype.Cargo}},
		},
		{"cargo install is a tool install", "RUN cargo install cargo-chef", nil},
		{
			"go behind environment assignments", "RUN CGO_ENABLED=0 GOOS=linux go build -o /out/app ./cmd/app",
			rebuilds{{Line: 1, Command: "CGO_ENABLED=0 GOOS=linux go build -o /out/app ./cmd/app", Ecosystem: projecttype.Go}},
		},
		{"go install is a tool install", "RUN /usr/local/go/bin/go install example.org/tool@v1.2.3", nil},
		{
			"the line is the one the instruction starts on",
			"FROM maven AS build\n# build the jar\nRUN cd /src \\\n    # skip the tests\n    && mvn \\\n       package\n",
			rebuilds{{Line: 3, Command: "mvn package", Ecosystem: projecttype.Maven}},
		},
		{"exec form", `RUN ["./gradlew", "build"]`, rebuilds{{Line: 1, Command: "./gradlew build", Ecosystem: projecttype.Gradle}}},
		{
			"exec form through a shell", `RUN ["/bin/sh", "-c", "npm ci && npm run build"]`,
			rebuilds{
				{Line: 1, Command: "npm ci", Ecosystem: projecttype.NPM, FetchOnly: true},
				{Line: 1, Command: "npm run build", Ecosystem: projecttype.NPM},
			},
		},
		{
			"here-document script", "RUN <<EOF\n# mvn install\ngo mod download\ngo build ./...\nEOF\nRUN cargo build",
			rebuilds{
				{Line: 1, Command: "go mod download", Ecosystem: projecttype.Go, FetchOnly: true},
				{Line: 1, Command: "go build ./...", Ecosystem: projecttype.Go},
				{Line: 6, Command: "cargo build", Ecosystem: projecttype.Cargo},
			},
		},
		{
			"dependencies are reported beside the build of their ecosystem only",
			"RUN mvn dependency:go-offline\nRUN npm ci\nRUN mvn package",
			rebuilds{
				{Line: 1, Command: "mvn dependency:go-offline", Ecosystem: projecttype.Maven, FetchOnly: true},
				{Line: 3, Command: "mvn package", Ecosystem: projecttype.Maven},
			},
		},
		{"dependencies alone", "RUN mvn dependency:go-offline\nRUN npm ci\nRUN cargo fetch\nRUN go mod download", nil},
		{"a comment that mentions a build", "FROM alpine\n# RUN mvn package would rebuild the jar\nCOPY app.jar /app.jar", nil},
		{"a here-document that is a file", "RUN cat <<EOF > /build.sh\nmvn package\nEOF", nil},
		{"other instructions", "LABEL build=\"mvn package\"\nCMD [\"go\", \"build\"]\nENV HINT=\"npm run build\"", nil},
		{"other uses of the tools", "RUN mvn --version && go version && npm run start && mvn install:install-file -Dfile=x.jar", nil},
		{"empty", "", nil},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := container.ContainerfileRebuilds(testCase.given)
			if testCase.want == nil {
				require.Empty(t, got)

				return
			}

			require.Equal(t, testCase.want, got)
		})
	}
}
