#!/bin/bash
# Wrapper for apt that guarantees non-interactive execution.
#
# Two problems this solves:
# 1. sudo env_reset strips DEBIAN_FRONTEND — set via --property=Environment
# 2. spiraldash.service uses ProtectSystem=strict which creates a read-only
#    mount namespace. systemd-run --pipe makes systemd (PID 1) start apt
#    in the root namespace, completely outside the dashboard's restrictions.
#    --pipe waits for completion and forwards stdout/stderr/exit code.
exec systemd-run --pipe --quiet \
    --property=Environment=DEBIAN_FRONTEND=noninteractive \
    --property=Environment=NEEDRESTART_MODE=a \
    -- /usr/bin/apt "$@"
