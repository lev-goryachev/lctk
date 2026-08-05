# LCTK reusable code-intel image.
#
# One versioned image is reused by every registered project, per ADR-0003. The
# project's source is mounted read-only at /workspace and per-project state lives
# in a named volume at /var/lib/lctk, so nothing project-specific is baked in.
#
# Both stages are pinned by digest so a rebuild produces the same content.

# The build stage compiles the per-project search and symbol service.
#
# Two things keep this code out of the portable host executable, both required by
# ADR-0011 and ADR-0019: Zoekt's low-level index package is Unix-specific and
# outside its stable public API, and the Tree-sitter symbol layer is reached
# through cgo. Neither may enter a Windows or macOS build, which is why they live
# in a separate module compiled here.
#
# The builder is Alpine rather than Debian because cgo is now required and the
# runtime stage is Alpine: linking against musl here is what allows the result to
# be one static binary on the same libc. ADR-0019 records that this revises the
# earlier position that no C toolchain enters the build; what that position was
# protecting -- a runtime image with no toolchain in it -- still holds.
FROM golang@sha256:56961d79ea8129efddcc0b8643fd8a5416b4e6228cfd477e3fd61deb2672c587 AS build

RUN apk add --no-cache gcc musl-dev

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY main.go ./
COPY internal ./internal
COPY cmd ./cmd
# Statically linked, so the runtime stage needs no libc shim and no compiler. The
# build cache matters more than it used to: a grammar is generated C and dominates
# a rebuild.
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=1 go build -trimpath -ldflags="-s -w -extldflags=-static" -o /out/lctk-code-intel ./

# The explicit test target gives local and release validation the exact Linux
# compiler environment used for the image without placing test code or a Go
# toolchain in the runtime image.
FROM build AS test

ARG TARGETARCH

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=1 go test ./... && \
    if [ "$TARGETARCH" = "amd64" ]; then CGO_ENABLED=1 go test -race ./internal/...; fi

# The release stress target exercises the production semantic schema and exact
# vector scan without adding measurement code to the runtime image.
FROM build AS stress-build

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=1 go build -trimpath -ldflags="-s -w -extldflags=-static" \
    -o /out/semantic-stress ./cmd/semantic-stress && \
    CGO_ENABLED=1 go build -trimpath -ldflags="-s -w -extldflags=-static" \
    -o /out/exact-stress ./cmd/exact-stress

FROM alpine@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce AS stress

COPY --from=stress-build /out/semantic-stress /usr/local/bin/semantic-stress
COPY --from=stress-build /out/exact-stress /usr/local/bin/exact-stress
ENTRYPOINT ["/usr/local/bin/semantic-stress"]

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
