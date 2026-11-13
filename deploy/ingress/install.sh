#!/bin/sh
set -eu
umask 077
PATH=/usr/sbin:/usr/bin:/sbin:/bin
export PATH

service=telemt-ingress.service
directory=/etc/telemt-ingress
current=$directory/haproxy.cfg
previous=$directory/haproxy.cfg.previous
unit=/etc/systemd/system/$service
reload=/usr/local/libexec/telemt-ingress-reload
bundle=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)

fail() {
    code=$1
    shift
    printf '%s\n' "$*" >&2
    exit "$code"
}

ready() {
    expected_capacity=${1:-}
    attempts=0
    while [ "$attempts" -lt 15 ]; do
        if systemctl is-active --quiet "$service" &&
            printf '@1 show info\n' | timeout 2 socat -t1 - UNIX-CONNECT:/run/telemt-ingress/master.sock 2>/dev/null |
                awk -v expected="$expected_capacity" '
                    $1 == "Pid:" && $2 > 0 { running = 1 }
                    $1 == "Maxconn:" && $2 == expected { matched = 1 }
                    END { exit !(running && (expected == "" || matched)) }
                '; then
            return 0
        fi
        attempts=$((attempts + 1))
        sleep 1
    done
    return 1
}

check_config() (
    cd "$directory"
    runuser -u haproxy -- /usr/sbin/haproxy -c -f "$1"
)

activate() {
    if systemctl is-active --quiet "$service"; then
        systemctl reload "$service" || return
    else
        systemctl start "$service" || return
    fi
}

[ "$(id -u)" -eq 0 ] || fail 11 'Run this command as root (sudo).'
[ -d /run/systemd/system ] || fail 11 'A running systemd is required.'
case "${1:-}" in
    packages|check|apply|apply-managed|status|rollback) action=$1 ;;
    *) fail 1 'Usage: sudo ./install.sh packages|check|apply|status|rollback' ;;
esac
[ "$#" -eq 1 ] || fail 1 'Expected exactly one action.'
if [ "$action" = apply-managed ]; then
    exec 3>&1
    exec 1>&2
fi

if [ "$action" = status ]; then
    ready || fail 1 'The Telemt ingress worker did not respond.'
    printf '@1 show stat\n' | timeout 5 socat -t3 - UNIX-CONNECT:/run/telemt-ingress/master.sock
    exit
fi

install -d -o root -g root -m 0700 /run/telemt-ingress-install
exec 9>/run/telemt-ingress-install/lock
flock -n 9 || fail 12 'Another Telemt ingress operation is running.'

if [ "$action" = packages ]; then
    if [ ! -x /usr/sbin/haproxy ] || [ ! -x /usr/bin/socat ]; then
        command -v apt-get >/dev/null || fail 11 'Install HAProxy 3.0+ and socat with the host package manager.'
        # Install only missing packages, keeping an existing HAProxy package in place.
        set --
        [ -x /usr/sbin/haproxy ] || set -- "$@" haproxy
        [ -x /usr/bin/socat ] || set -- "$@" socat
        apt-get update
        DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "$@"
    fi
    /usr/sbin/haproxy -v | awk 'NR == 1 { split($3, version, "."); ok = version[1] + 0 >= 3 } END { exit !ok }' ||
        fail 11 'HAProxy 3.0 or newer is required.'
    exit
fi

[ -x /usr/sbin/haproxy ] && [ -x /usr/bin/socat ] || fail 1 'Run sudo ./install.sh packages first.'
getent passwd haproxy >/dev/null || fail 11 'The haproxy OS user is required.'
[ ! -e "$unit" ] || cmp -s "$unit" "$bundle/$service" || fail 12 "Existing $unit differs. Review it before migrating this service."
[ ! -e "$reload" ] || cmp -s "$reload" "$bundle/telemt-ingress-reload" || fail 12 "Existing $reload differs. Review it before migrating this service."
source_config=$bundle/haproxy.cfg
if [ "$action" = apply-managed ] && [ -f "$current" ]; then
    owner=$(head -n 1 "$source_config")
    [ "$(head -n 1 "$current")" = "$owner" ] ||
        fail 12 'The existing configuration belongs to another cluster or has no cluster marker.'
    old_revision=$(sed -n '2s/^# Cluster revision: //p' "$current")
    revision=$(sed -n '2s/^# Cluster revision: //p' "$source_config")
    [ "$revision" -ge "$old_revision" ] 2>/dev/null ||
        fail 12 'A newer cluster revision is already installed.'
fi
if [ "$action" = rollback ]; then
    [ -f "$previous" ] || fail 1 'No previous Telemt ingress configuration is available.'
    source_config=$previous
fi

if [ ! -d "$directory" ]; then
    install -d -o root -g haproxy -m 0750 "$directory"
fi
candidate=$(mktemp "$directory/.haproxy.cfg.XXXXXXXX")
backup=''
was_active=0
new_unit=0
replaced=0

restore() {
    if [ -n "$backup" ]; then
        mv -f -- "$backup" "$current" || return
    else
        rm -f -- "$current" || return
    fi
    if [ "$was_active" -eq 1 ]; then
        activate && ready || return
    else
        systemctl stop "$service" || return
    fi
    replaced=0
}

cleanup() {
    result=$?
    trap - EXIT
    if [ "$replaced" -eq 1 ]; then
        restore || result=25
    fi
    if [ "$new_unit" -eq 1 ] && [ "$result" -ne 0 ] && [ "$replaced" -eq 0 ]; then
        systemctl disable "$service" >/dev/null 2>&1 || true
        rm -f -- "$unit" "$reload"
        systemctl daemon-reload || true
    fi
    rm -f -- "$candidate" ${backup:+"$backup"}
    exit "$result"
}
trap cleanup EXIT
trap 'exit 25' HUP INT TERM

if [ "$action" = apply-managed ]; then
    # Keep room for the kernel, other services and workers draining after reload.
    # A TLS/HTTP connection budget is twice the Native forwarding estimate.
    connection_kib=96
    if grep -qx 'frontend web' "$source_config"; then connection_kib=192; fi
    capacity=$(awk -v per_connection="$connection_kib" '
        function min(a, b) { return a < b ? a : b }
        $1 == "MemTotal:" { total = $2 }
        $1 == "MemAvailable:" { available = $2 }
        END {
            budget = min(total / 4, available / 2)
            if ((getline group < "/proc/self/cgroup") > 0 && group ~ /^0::\//) {
                sub(/^0::/, "", group)
                # A cgroup v2 namespace can expose the current group as /.
                while (1) {
                    sub(/\/$/, "", group)
                    limit_path = "/sys/fs/cgroup" group "/memory.max"
                    usage_path = "/sys/fs/cgroup" group "/memory.current"
                    if ((getline limit < limit_path) > 0 && limit ~ /^[0-9]+$/ &&
                        (getline usage < usage_path) > 0 && usage ~ /^[0-9]+$/) {
                        budget = min(budget, min(limit / 4096, (limit - usage) / 2048))
                    }
                    close(limit_path)
                    close(usage_path)
                    if (group == "") break
                    sub(/\/[^/]*$/, "", group)
                }
            }
            printf "%d\n", int(min(budget / per_connection, 100000))
        }' /proc/meminfo)
    [ "$capacity" -ge 256 ] || fail 14 'Insufficient free memory to start the ingress worker.'
    sed "s/^    maxconn 10000$/    maxconn $capacity/" "$source_config" > "$candidate"
    grep -qx "    maxconn $capacity" "$candidate" || fail 13 'Missing connection limit.'
    chown root:haproxy "$candidate"
    chmod 0640 "$candidate"
else
    install -o root -g haproxy -m 0640 "$source_config" "$candidate"
fi
check_config "$candidate" || fail 13 'HAProxy rejected the configuration.'
[ "$action" != check ] || exit 0

if [ "$action" = apply-managed ]; then
    printf 'ready %s\n' "$capacity" >&3
    IFS= read -r decision && [ "$decision" = commit ] || exit 0
fi

if [ ! -e "$unit" ]; then
    new_unit=1
    install -d -m 0755 /usr/local/libexec
    install -o root -g root -m 0755 "$bundle/telemt-ingress-reload" "$reload"
    install -o root -g root -m 0644 "$bundle/$service" "$unit"
    systemctl daemon-reload
fi
changed=1
if [ -f "$current" ]; then
    if cmp -s "$candidate" "$current"; then
        changed=0
    fi
    backup=$(mktemp "$directory/.haproxy.backup.XXXXXXXX")
    cp -p -- "$current" "$backup"
fi
if systemctl is-active --quiet "$service"; then
    was_active=1
fi
# Arm restoration before replacing the destination so a signal cannot leave
# a newly written file outside the activation transaction.
replaced=1
mv -f -- "$candidate" "$current"
if ! activate; then
    fail 24 'Activation failed. Restoring the previous configuration.'
fi
systemctl enable "$service" || fail 24 'Could not enable the service. Restoring the previous configuration.'
ready "${capacity:-}" || fail 24 'The worker did not confirm the configuration. Restoring the previous file.'
replaced=0
if [ -n "$backup" ] && [ "$changed" -eq 1 ]; then
    mv -f -- "$backup" "$previous" || fail 25 'The worker is running but the previous file could not be retained.'
fi
printf '%s\n' 'Telemt ingress worker is active. Run status and verify client traffic.'
