# LCTK reusable code-intel image.
#
# One versioned image is reused by every registered project, per ADR-0003. The
# project's source is mounted read-only at /workspace and per-project state lives
# in a named volume at /var/lib/lctk, so nothing project-specific is baked in.
#
# Both stages are pinned by digest so a rebuild produces the same content.

# The build stage compiles the per-project search service. Zoekt's low-level
# index package is Unix-specific and outside its stable public API, which is why
# that code lives in a separate module and is compiled here rather than on the
# host, per ADR-0011.
FROM golang@sha256:298734aec230b5f3e8cee450ce6d7eccc39f1797ba548ee90d57e9803030c6c3 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY main.go ./
COPY internal ./internal
# CGO is off so the result is a static binary that runs on the Alpine stage
# without a libc shim, and so no C toolchain enters the supply chain.
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/lctk-code-intel ./

FROM alpine@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce

LABEL org.opencontainers.image.title="LCTK code-intel" \
      org.opencontainers.image.description="Reusable per-project code intelligence runtime" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.source="https://github.com/lev-goryachev/lctk"

COPY --from=build /out/lctk-code-intel /usr/local/bin/lctk-code-intel
COPY entrypoint.sh /usr/local/bin/lctk-entrypoint
RUN chmod +x /usr/local/bin/lctk-entrypoint

# The state directory is a mount point for the project volume. Creating it here
# means the container starts correctly even if the volume is empty.
RUN mkdir -p /var/lib/lctk /workspace

# The search service listens here. The port is fixed inside the container; the
# host publishes it on an ephemeral loopback port, so two projects never collide.
EXPOSE 8080

# Readiness is a file in the persistent volume, written by the service once its
# listener is accepting. Health therefore reflects the container having completed
# its own startup rather than merely being up.
HEALTHCHECK --interval=5s --timeout=3s --start-period=2s --retries=6 \
    CMD test -f /var/lib/lctk/ready

ENTRYPOINT ["/usr/local/bin/lctk-entrypoint"]
