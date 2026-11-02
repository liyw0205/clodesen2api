#!/usr/bin/env bash
set -euo pipefail

PROJECT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
BINARY_PATH="${BINARY_PATH:-$PROJECT_DIR/bin/clodesen2api}"
CONFIG_PATH="${CONFIG_PATH:-$PROJECT_DIR/config.toml}"
LOG_PATH="${LOG_PATH:-$PROJECT_DIR/run.log}"
PID_PATH="${PID_PATH:-$PROJECT_DIR/run.pid}"

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

if [[ ! -x "$BINARY_PATH" ]]; then
    printf 'binary is missing or not executable: %s\n' "$BINARY_PATH" >&2
    exit 1
fi
cd "$PROJECT_DIR"

declare -a existing_pids=()
mkdir -p "$(dirname -- "$PID_PATH")" "$(dirname -- "$LOG_PATH")"
while IFS= read -r pid; do
    [[ -n "$pid" ]] && existing_pids+=("$pid")
done < <(find_project_pids)

if ((${#existing_pids[@]} > 0)); then
    printf '%s\n' "${existing_pids[0]}" > "$PID_PATH"
    printf 'clodesen2api is already running (pid: %s)\n' "${existing_pids[*]}"
    exit 0
fi

touch "$LOG_PATH"

nohup "$BINARY_PATH" -c "$CONFIG_PATH" >> "$LOG_PATH" 2>&1 < /dev/null &
pid=$!
printf '%s\n' "$pid" > "$PID_PATH"

for ((attempt = 0; attempt < 20; attempt++)); do
    if is_project_process "$pid"; then
        printf 'clodesen2api started (pid: %s, log: %s)\n' "$pid" "$LOG_PATH"
        exit 0
    fi
    if ! kill -0 "$pid" 2>/dev/null; then
        if [[ "$(read_pid_file 2>/dev/null || true)" == "$pid" ]]; then
            rm -f "$PID_PATH"
        fi
        printf 'clodesen2api failed to start; see %s\n' "$LOG_PATH" >&2
        tail -n 20 "$LOG_PATH" >&2 || true
        exit 1
    fi
    sleep 0.1
done

printf 'clodesen2api did not become ready; see %s\n' "$LOG_PATH" >&2
exit 1
