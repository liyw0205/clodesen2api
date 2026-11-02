#!/usr/bin/env bash
set -euo pipefail

PROJECT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
BINARY_PATH="${BINARY_PATH:-$PROJECT_DIR/bin/clodesen2api}"
PID_PATH="${PID_PATH:-$PROJECT_DIR/run.pid}"
STOP_TIMEOUT_SECONDS="${STOP_TIMEOUT_SECONDS:-15}"

is_project_process() {
    local pid="$1" exe
    [[ "$pid" =~ ^[1-9][0-9]*$ ]] || return 1
    [[ -d "/proc/$pid" ]] || return 1
    exe="$(readlink "/proc/$pid/exe" 2>/dev/null || true)"
    [[ "$exe" == "$BINARY_PATH" || "$exe" == "$BINARY_PATH (deleted)" ]]
}

find_project_pids() {
    local proc pid exe
    shopt -s nullglob
    for proc in /proc/[0-9]*; do
        pid="${proc#/proc/}"
        exe="$(readlink "$proc/exe" 2>/dev/null || true)"
        if [[ "$exe" == "$BINARY_PATH" || "$exe" == "$BINARY_PATH (deleted)" ]]; then
            printf '%s\n' "$pid"
        fi
    done
}

read_pid_file() {
    local pid
    [[ -r "$PID_PATH" ]] || return 1
    IFS= read -r pid < "$PID_PATH" || return 1
    [[ "$pid" =~ ^[1-9][0-9]*$ ]] || return 1
    printf '%s\n' "$pid"
}

if [[ ! "$STOP_TIMEOUT_SECONDS" =~ ^[1-9][0-9]*$ ]]; then
    printf 'STOP_TIMEOUT_SECONDS must be a positive integer\n' >&2
    exit 1
fi

declare -a pids=()
add_pid() {
    local candidate="$1" pid
    for pid in "${pids[@]}"; do
        [[ "$pid" == "$candidate" ]] && return
    done
    pids+=("$candidate")
}

pid_from_file="$(read_pid_file 2>/dev/null || true)"
if [[ -n "$pid_from_file" ]] && is_project_process "$pid_from_file"; then
    add_pid "$pid_from_file"
fi
while IFS= read -r pid; do
    [[ -n "$pid" ]] && add_pid "$pid"
done < <(find_project_pids)

if ((${#pids[@]} == 0)); then
    [[ -f "$PID_PATH" ]] && rm -f "$PID_PATH"
    printf 'clodesen2api is not running\n'
    exit 0
fi

for pid in "${pids[@]}"; do
    if is_project_process "$pid"; then
        printf 'stopping clodesen2api (pid: %s)\n' "$pid"
        kill -TERM "$pid"
    fi
done

attempts=$((STOP_TIMEOUT_SECONDS * 10))
for ((attempt = 0; attempt < attempts; attempt++)); do
    running=0
    for pid in "${pids[@]}"; do
        if is_project_process "$pid"; then
            running=1
            break
        fi
    done
    if ((running == 0)); then
        break
    fi
    sleep 0.1
done

for pid in "${pids[@]}"; do
    if is_project_process "$pid"; then
        printf 'clodesen2api did not stop within %ss (pid: %s)\n' "$STOP_TIMEOUT_SECONDS" "$pid" >&2
        exit 1
    fi
done

if [[ -n "$pid_from_file" && "$(read_pid_file 2>/dev/null || true)" == "$pid_from_file" ]]; then
    rm -f "$PID_PATH"
fi
printf 'clodesen2api stopped\n'
