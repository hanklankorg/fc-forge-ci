# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0
#
# The runtime image: the jar the release built, on a Java runtime. Nothing
# runs at build time, so every platform builds without CPU emulation.
FROM docker.io/library/eclipse-temurin:25-jre@sha256:dbb43d81099eec91e0800226ed2296d03146ee7857165b95112372ad0d67e87f

# Readable whatever umask the build ran under: GitLab's release build runs with
# 077, which would leave the jar unreadable to the runtime user.
COPY --chmod=0444 target/hello-maven-service-*.jar /app/app.jar

USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["java", "-jar", "/app/app.jar"]
