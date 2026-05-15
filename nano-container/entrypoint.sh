#!/usr/bin/env bash
# Container entrypoint for the Exasol Nano DB AppDir.
#
# Why a wrapper:
#   The DB's run.sh treats EOF on stdin (Ctrl-D) as the documented clean-
#   shutdown trigger. `podman run -d` connects the container's stdin to
#   /dev/null, which produces EOF immediately and shuts the DB down. We keep
#   stdin open via a FIFO whose write end is held by a long-running `tail`,
#   and on SIGTERM/SIGINT we close the FIFO write end so the DB gets the
#   clean-shutdown signal it expects.

set -euo pipefail

DB_RUN="/opt/exasol-nano/run.sh"
STDIN_FIFO="$(mktemp -u /tmp/db-stdin.XXXXXX)"
DB_PID=""
TAIL_PID=""
ESCALATOR_PID=""
SHUTDOWN_REQUESTED=0

cleanup_tail() {
  if [[ -n "$TAIL_PID" ]] && kill -0 "$TAIL_PID" 2>/dev/null; then
    kill "$TAIL_PID" 2>/dev/null || true
    wait "$TAIL_PID" 2>/dev/null || true
  fi
  TAIL_PID=""
  rm -f "$STDIN_FIFO"
}

cleanup_escalator() {
  if [[ -n "$ESCALATOR_PID" ]] && kill -0 "$ESCALATOR_PID" 2>/dev/null; then
    kill "$ESCALATOR_PID" 2>/dev/null || true
    wait "$ESCALATOR_PID" 2>/dev/null || true
  fi
  ESCALATOR_PID=""
}

graceful_stop() {
  (( SHUTDOWN_REQUESTED == 1 )) && return
  SHUTDOWN_REQUESTED=1

  # Close the FIFO write end -> DB sees EOF -> starts clean shutdown.
  cleanup_tail

  # Escalate to SIGTERM, then SIGKILL, if the DB doesn't exit on its own.
  if [[ -n "$DB_PID" ]] && kill -0 "$DB_PID" 2>/dev/null; then
    (
      sleep 60
      if kill -0 "$DB_PID" 2>/dev/null; then
        kill -TERM "$DB_PID" 2>/dev/null || true
        sleep 10
        if kill -0 "$DB_PID" 2>/dev/null; then
          kill -KILL "$DB_PID" 2>/dev/null || true
        fi
      fi
    ) &
    ESCALATOR_PID=$!
  fi
}

trap graceful_stop TERM INT

mkfifo "$STDIN_FIFO"
tail -f /dev/null > "$STDIN_FIFO" &
TAIL_PID=$!

"$DB_RUN" < "$STDIN_FIFO" &
DB_PID=$!

set +e
wait "$DB_PID"
EXIT_CODE=$?
set -e

cleanup_escalator
cleanup_tail
exit "$EXIT_CODE"
