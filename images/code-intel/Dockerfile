# LCTK reusable code-intel image.
#
# One versioned image is reused by every registered project, per ADR-0003. The
# project's source is mounted read-only at /workspace and per-project state lives
# in a named volume at /var/lib/lctk, so nothing project-specific is baked in.
#
# The base image is pinned by digest so a rebuild produces the same content.
FROM alpine@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce

# Slice 1.2 carries no search or language tooling yet. The image exists to prove
# the project runtime boundary: isolated mount, network, volume, lifecycle, and
# health. Backends arrive in later slices.
LABEL org.opencontainers.image.title="LCTK code-intel" \
      org.opencontainers.image.description="Reusable per-project code intelligence runtime" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.source="https://github.com/lev-goryachev/lctk"

COPY entrypoint.sh /usr/local/bin/lctk-entrypoint
RUN chmod +x /usr/local/bin/lctk-entrypoint

# The state directory is a mount point for the project volume. Creating it here
# means the container starts correctly even if the volume is empty.
RUN mkdir -p /var/lib/lctk /workspace

# Readiness is a file in the persistent volume, so the health status reflects the
# container having completed its own startup rather than merely being up.
HEALTHCHECK --interval=5s --timeout=3s --start-period=2s --retries=6 \
    CMD test -f /var/lib/lctk/ready

ENTRYPOINT ["/usr/local/bin/lctk-entrypoint"]
