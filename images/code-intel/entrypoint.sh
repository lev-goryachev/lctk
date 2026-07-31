#!/bin/sh
# Entrypoint for the LCTK reusable code-intel image.
#
# Slice 1.2 proves the project runtime boundary rather than providing code
# intelligence. The startup sequence records enough state in the persistent
# volume for a test to demonstrate that stop/start preserves it.
set -eu

STATE_DIR=/var/lib/lctk
WORKSPACE=/workspace

# Readiness is cleared first so a restart cannot report healthy before this
# startup has finished.
rm -f "$STATE_DIR/ready"
mkdir -p "$STATE_DIR"

# created_at is written once and must survive every later start. It is the marker
# that distinguishes a preserved volume from a fresh one.
if [ ! -f "$STATE_DIR/created_at" ]; then
    date -u +%Y-%m-%dT%H:%M:%SZ >"$STATE_DIR/created_at"
fi

# starts increments on every container start, so a test can prove the same volume
# was reused rather than recreated.
starts=0
if [ -f "$STATE_DIR/starts" ]; then
    starts=$(cat "$STATE_DIR/starts")
fi
echo $((starts + 1)) >"$STATE_DIR/starts"

# The project identity comes from the environment, which the host daemon sets
# from the registry. It is recorded so that a mismatch between the volume and the
# route is detectable rather than silent.
printf '%s\n' "${LCTK_PROJECT_ID:-unknown}" >"$STATE_DIR/project_id"

# Record what the mount actually looks like from inside the container. A project
# must never be able to write to its own source through the code-intel boundary.
if [ -d "$WORKSPACE" ]; then
    if touch "$WORKSPACE/.lctk-write-probe" 2>/dev/null; then
        rm -f "$WORKSPACE/.lctk-write-probe"
        printf 'writable\n' >"$STATE_DIR/workspace_mode"
    else
        printf 'read-only\n' >"$STATE_DIR/workspace_mode"
    fi
else
    printf 'missing\n' >"$STATE_DIR/workspace_mode"
fi

touch "$STATE_DIR/ready"

# Stay alive so the stack has a long-running service to report health for, and so
# a stop is a real container stop rather than an exit.
exec tail -f /dev/null
