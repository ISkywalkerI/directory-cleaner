#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
[[ $(id -u) == 0 ]] || { echo 'systemd acceptance requires root' >&2; exit 1; }
[[ $(cat /proc/1/comm) == systemd ]] || { echo 'A running systemd host is required' >&2; exit 1; }
exe=$(realpath "${1:-artifacts/dircleaner}")
report=$(realpath -m artifacts/systemd)
mkdir -p "$report"
root=$(mktemp -d /opt/directory-cleaner-acceptance.XXXXXXXX)
name=$(basename "$root")
unit="$name.service"
user="dc-${name##*.}"
installed=false
user_created=false
cleanup() {
    status=$?
    trap - EXIT
    if $installed; then
        systemctl status "$unit" --no-pager >"$report/status.txt" 2>&1 || true
        journalctl -u "$unit" --no-pager >"$report/journal.txt" 2>&1 || true
        systemctl disable --now "$unit" || true
        rm -f "/etc/systemd/system/$unit"
        systemctl daemon-reload || true
        systemctl reset-failed "$unit" 2>/dev/null || true
    fi
    cp -r "$root/logs/." "$report/" 2>/dev/null || true
    if $user_created; then userdel "$user" || true; fi
    # mktemp created this unique root; never accept a caller-provided deletion path.
    [[ $root == /opt/directory-cleaner-acceptance.* ]] && rm -rf -- "$root"
    exit "$status"
}
trap cleanup EXIT
wait_for() {
    local deadline=$((SECONDS + 30))
    until "$@"; do
        if (( SECONDS >= deadline )); then echo "Timed out: $*" >&2; return 1; fi
        sleep 0.1
    done
}
audit_files() { find "$root/logs" -maxdepth 1 -type f -name 'dir-cleaner-*.jsonl' -print | sort; }
audit_count() { find "$root/logs" -maxdepth 1 -type f -name 'dir-cleaner-*.jsonl' -exec cat {} + 2>/dev/null | wc -l; }
has_scans() { [[ -n $(audit_files) ]] && (( $(audit_count) >= 6 )); }
main_pid() { systemctl show "$unit" --property=MainPID --value; }
resumed() { [[ $(main_pid) != 0 && $(main_pid) != "$1" ]] && (( $(audit_count) > "$2" )); }
write_config() {
    cat >"$root/config.yaml" <<EOF
version: 1
defaults:
  scan_interval: 200ms
  stable_for: ${2:-0s}
  min_age: 0s
  dry_run: $1
  audit_log_dir: $root/logs
directories:
- path: $root/data
  trigger_bytes: 10
  target_bytes: 4
EOF
    chmod 644 "$root/config.yaml"
}
stop_clean() {
    local pid length
    pid=$(main_pid)
    [[ $pid != 0 ]]
    timeout 10s systemctl stop "$unit"
    [[ $(systemctl show "$unit" --property=ExecMainStatus --value) == 0 ]]
    [[ $(systemctl show "$unit" --property=Result --value) == success ]]
    [[ $(systemctl show "$unit" --property=ActiveState --value) == inactive ]]
    ! kill -0 "$pid" 2>/dev/null
    length=$(find "$root/logs" -maxdepth 1 -type f -name 'dir-cleaner-*.jsonl' -printf '%s\n' 2>/dev/null | awk '{sum += $1} END {print sum + 0}')
    sleep 0.5
    [[ $(find "$root/logs" -maxdepth 1 -type f -name 'dir-cleaner-*.jsonl' -printf '%s\n' 2>/dev/null | awk '{sum += $1} END {print sum + 0}') == "$length" ]]
}
useradd --system --user-group --no-create-home --shell /usr/sbin/nologin "$user"
user_created=true
chmod 755 "$root"
mkdir "$root/data" "$root/logs"
cp "$exe" "$root/dircleaner"
chmod 755 "$root/dircleaner"
for pair in 'old 3' 'middle 2' 'new 1'; do
    read -r file hours <<<"$pair"
    printf 1234 >"$root/data/$file"
    touch -d "$hours hours ago" "$root/data/$file"
done
chown -R "$user:$user" "$root/data" "$root/logs"
write_config true
sed -e '/^\[Unit\]/a StartLimitIntervalSec=0' \
    -e "s|/opt/directory-cleaner|$root|g" \
    -e "s|/etc/directory-cleaner/config.yaml|$root/config.yaml|g" \
    -e "s|/var/log/directory-cleaner|$root/logs|g" \
    -e "s|^User=.*|User=$user|" -e "s|^Group=.*|Group=$user|" \
    deploy/directory-cleaner.service >"/etc/systemd/system/$unit"
installed=true
systemd-analyze verify "/etc/systemd/system/$unit"
systemctl daemon-reload
systemctl enable --now "$unit"
systemctl is-enabled "$unit"
systemctl is-active "$unit"
wait_for has_scans
[[ $(find "$root/data" -type f | wc -l) == 3 ]]
stop_clean
systemctl start "$unit"
pid=$(main_pid)
count=$(audit_count)
systemctl restart "$unit"
wait_for resumed "$pid" "$count"
! kill -0 "$pid" 2>/dev/null
pid=$(main_pid)
count=$(audit_count)
kill -KILL "$pid"
wait_for resumed "$pid" "$count"
stop_clean
write_config true 60s
systemctl start "$unit"
sleep 0.5
stop_clean
find "$root/logs" -maxdepth 1 -type f -name 'dir-cleaner-*.jsonl' -exec cat {} + | tail -n 1 | grep -q '"cancelled"'
systemctl reset-failed "$unit"
write_config false
systemctl start "$unit"
deleted() { find "$root/logs" -maxdepth 1 -type f -name 'dir-cleaner-*.jsonl' -exec cat {} + | grep -q '"deleted_bytes":8,"deleted_files":2'; }
wait_for deleted
stop_clean
[[ $(find "$root/data" -type f | wc -l) == 1 && $(cat "$root/data/new") == 1234 ]]
systemctl disable "$unit"
rm "/etc/systemd/system/$unit"
systemctl daemon-reload
[[ $(systemctl show "$unit" --property=LoadState --value) == not-found ]]
printf 'PASS: install, preview, stop/start/restart, crash recovery, graceful cancellation, delete, uninstall\n' >"$report/result.txt"
