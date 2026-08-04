# Tracked execution boundary for the Slice 4.1 symbol-backend evaluation.
#
# Both candidates need Linux: tree-sitter is reached through cgo, and Universal
# Ctags is a native executable. Building and running here rather than on the
# developer's machine is what makes the measurement reproducible, and it is the
# same boundary the search-backend evaluation used for the same reason.
#
# The build image is pinned by digest to the one this repository already uses.
FROM golang@sha256:298734aec230b5f3e8cee450ce6d7eccc39f1797ba548ee90d57e9803030c6c3

# git clones the pinned corpus; universal-ctags is one of the two candidates.
RUN apt-get update \
 && apt-get install -y --no-install-recommends universal-ctags git ca-certificates \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY corpus ./corpus
COPY engines ./engines
COPY cmd ./cmd

# cgo is required. The production decision about static linking is not this
# harness's problem: it measures capability and cost, and the image it runs in is
# not the image LCTK ships.
#
# The caches matter here more than they usually would: every grammar is generated
# C, and recompiling all of them on each edit to a query makes the harness
# unpleasant enough to stop using.
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=1 go build -trimpath -o /usr/local/bin/symbol-eval ./cmd/symbol-eval

ENTRYPOINT ["/usr/local/bin/symbol-eval"]
