# Gateway evaluation harness

This directory contains the test upstreams, official MCP Go client probes, benchmark command, and minimal custom gateway used for the Slice 0.2 comparison. It is evidence code, not a production package or an accepted gateway implementation.

The custom candidate is intentionally narrow. It binds project scope to `/projects/{project_id}/mcp`, validates a project grant before dispatch, strips the external grant before proxying to the project service, supports live registration/removal, and translates connection failures at the HTTP boundary. It does not implement the production registry, secret storage, persistence, lifecycle reconciliation, observability, or migration contracts.

Run its automated contract tests from the repository root:

```text
go test ./spikes/gateway-evaluation
```

Build the Linux image input from the repository root, then build the local image:

```text
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o spikes/gateway-evaluation/bin/gateway-evaluation-linux-amd64 ./spikes/gateway-evaluation
docker build -t lctk-gateway-spike:harness spikes/gateway-evaluation
```

The candidate-specific containers and credentials used during measurement are deliberately not tracked. Immutable upstream revisions, image digests, environment notes, and measurements are recorded in the evaluation results document.
