set -eu
export PATH=/usr/sbin:/usr/bin:/sbin:/bin
umask 077
stage=$1
arch=$2
root=/opt/telemt-node
config=/var/lib/private/telemt-node/telemt.toml
pending=$root/.panel-upgrade-pending
backup=$root/upgrade-backup.${stage##*.}
proxy=telemt.service
agent=telemt-agent.service
gateway=telemt-node-gateway.service
system=/etc/systemd/system
socket=telemt-node-control.socket
helper=telemt-node-control@.service
helper_probe=telemt-node-control@panel-probe.service
[ "$(id -u)" = 0 ] && [ "$(uname -m)" = "$arch" ] || exit 11
exec 8>/run/telemt-node-install.lock
flock -n 8 || exit 14
[ ! -e "$pending" ] && [ ! -L "$pending" ] || exit 25
[ "$(stat -c '%u:%a' "$root")" = 0:755 ] && [ ! -L "$root" ] || exit 11
[ -f "$config" ] && [ ! -L "$config" ] && [ "$(readlink -f "$config")" = "$config" ] || exit 11
check_unit() {
    local unit=$1 expected=$2
    [ "$(systemctl show "$unit" -p FragmentPath --value)" = "$system/$unit" ] &&
        [ -z "$(systemctl show "$unit" -p DropInPaths --value)" ] &&
        [[ "$(systemctl show "$unit" -p ExecStart --value)" == "{ path=$expected ; "* ]] &&
        systemctl is-active --quiet "$unit"
}
check_unit "$proxy" "$root/telemt" &&
    check_unit "$agent" "$root/node-runtime" &&
    check_unit "$gateway" "$root/node-runtime" || exit 11
for name in telemt node-runtime; do
    [ -f "$root/$name" ] && [ ! -L "$root/$name" ] &&
        [ "$(stat -c '%u:%a' "$root/$name")" = 0:755 ] || exit 11
done

(cd "$stage" && sha256sum -c checksums >/dev/null) || exit 11
selected=()
stop_units=()
start_units=()
if [ -f "$stage/node-runtime" ] && ! cmp -s "$stage/node-runtime" "$root/node-runtime"; then
    selected+=(node-runtime)
    stop_units+=("$gateway" "$agent")
    start_units+=("$agent" "$gateway")
fi
if [ -f "$stage/telemt" ] && ! cmp -s "$stage/telemt" "$root/telemt"; then
    selected+=(telemt)
    stop_units+=("$proxy")
    start_units=("$proxy" "${start_units[@]}")
fi
repair=0
new_socket=0
file_mode() {
    [ -f "$1" ] && [ ! -L "$1" ] && [ "$(stat -c '%u:%a' "$1")" = "0:$2" ]
}
absent() { [ ! -e "$1" ] && [ ! -L "$1" ]; }
unit_ref() {
    if [ "$1" = "$helper" ]; then printf '%s' "$helper_probe"; else printf '%s' "$1"; fi
}
if [ -f "$stage/legacy-agent.toml" ]; then
    # Accept only profiles written by the SSH installer. Leave local edits,
    # existing credentials and the proxy/gateway units under their owners' control.
    file_mode "$root/agent.toml" 600 && file_mode "$system/$agent" 644 || exit 11
    if cmp -s "$root/agent.toml" "$stage/legacy-agent.toml" &&
       cmp -s "$system/$agent" "$stage/legacy-agent.service"; then
        absent "$root/service-control" && absent /run/telemt-node-control || exit 11
        for unit in "$socket" "$helper"; do
            absent "$system/$unit" &&
                [ "$(systemctl show "$(unit_ref "$unit")" -p LoadState --value)" = not-found ] || exit 11
        done
        new_socket=1
    else
        if ! { cmp -s "$root/agent.toml" "$stage/basic-agent.toml" &&
               cmp -s "$system/$agent" "$stage/basic-agent.service"; } &&
           ! { cmp -s "$root/agent.toml" "$stage/agent.toml" &&
               cmp -s "$system/$agent" "$stage/$agent"; }; then
            exit 11
        fi
        for unit in "$socket" "$helper"; do
            file_mode "$system/$unit" 644 && cmp -s "$system/$unit" "$stage/$unit" &&
                [ "$(systemctl show "$(unit_ref "$unit")" -p FragmentPath --value)" = "$system/$unit" ] &&
                [ -z "$(systemctl show "$(unit_ref "$unit")" -p DropInPaths --value)" ] || exit 11
        done
        file_mode "$root/service-control" 500 &&
            { cmp -s "$root/service-control" "$stage/service-control" ||
              cmp -s "$root/service-control" "$stage/basic-service-control"; } || exit 11
    fi
    repair=1
    if [[ " ${stop_units[*]} " != *" $agent "* ]]; then
        stop_units=("$agent" "${stop_units[@]}")
        start_units+=("$agent")
    fi
fi
[ "${#selected[@]}" != 0 ] || [ "$repair" = 1 ] || exit 26
[ ! -e "$backup" ] && [ ! -L "$backup" ] || exit 11
mkdir -m 0700 -- "$pending"

stopping=0
snapshot=0
committed=0
aborted=0
socket_published=0
socket_enabled=0
units_active() {
    systemctl is-active --quiet "$proxy" && systemctl is-active --quiet "$agent" &&
        systemctl is-active --quiet "$gateway" &&
        { [ "$repair" = 0 ] || systemctl is-active --quiet "$socket"; }
}
replace() {
    local source=$1 destination=$2 temporary
    temporary=$(mktemp "${destination%/*}/.panel-upgrade.XXXXXXXXXX") || return 1
    if ! cp -a -- "$source" "$temporary" || ! mv -Tf -- "$temporary" "$destination"; then
        rm -f -- "$temporary"
        return 1
    fi
}
cleanup() {
    local code=$? restored=1
    trap - EXIT
    trap '' HUP INT TERM
    set +e
    if [ "$committed" = 0 ] && [ "$stopping" = 1 ]; then
        if [ "$snapshot" = 1 ]; then
            if ! cmp -s "$pending/telemt.toml" "$config" && [[ " ${stop_units[*]} " != *" $proxy "* ]]; then
                stop_units+=("$proxy")
                start_units=("$proxy" "${start_units[@]}")
            fi
            if systemctl stop "${stop_units[@]}" >/dev/null 2>&1; then
                local name
                for name in "${selected[@]}"; do
                    replace "$pending/$name" "$root/$name" || restored=0
                done
                if ! cmp -s "$pending/telemt.toml" "$config"; then
                    replace "$pending/telemt.toml" "$config" || restored=0
                fi
                if [ "$repair" = 1 ]; then
                    if [ "$socket_enabled" = 1 ]; then
                        systemctl disable --now "$socket" >/dev/null 2>&1 || restored=0
                    fi
                    if [ "$socket_published" = 1 ]; then
                        rm -f -- "$root/service-control" "$system/$socket" "$system/$helper" || restored=0
                        if [ -d /run/telemt-node-control ]; then
                            rmdir -- /run/telemt-node-control || restored=0
                        fi
                    elif [ "$new_socket" = 0 ]; then
                        replace "$pending/service-control" "$root/service-control" || restored=0
                    fi
                    replace "$pending/agent.toml" "$root/agent.toml" &&
                        replace "$pending/$agent" "$system/$agent" &&
                        systemctl daemon-reload >/dev/null 2>&1 || restored=0
                    cmp -s "$pending/agent.toml" "$root/agent.toml" &&
                        cmp -s "$pending/$agent" "$system/$agent" || restored=0
                fi
            else
                restored=0
            fi
        fi
        # A crashing candidate can consume the unit's systemd start budget.
        systemctl reset-failed "${start_units[@]}" >/dev/null 2>&1 || true
        # The legacy profile did not have a control socket.
        repair=0
        systemctl start "${start_units[@]}" >/dev/null 2>&1 && units_active || restored=0
        if [ "$snapshot" = 1 ]; then
            cmp -s "$pending/telemt" "$root/telemt" &&
                cmp -s "$pending/node-runtime" "$root/node-runtime" &&
                cmp -s "$pending/telemt.toml" "$config" || restored=0
        fi
        if [ "$restored" = 1 ] && [ "$aborted" = 0 ]; then code=24; fi
    fi
    if [ "$restored" = 0 ]; then exit 25; fi
    # Keep the snapshot on success or verified rollback. An interrupted
    # transaction leaves the fixed pending directory as an interlock.
    if [ "$snapshot" = 1 ]; then
        mv -T -- "$pending" "$backup" || exit 25
    else
        rm -rf -- "$pending" || exit 25
    fi
    exit "$code"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

for name in "${selected[@]}"; do
    install -m 0755 "$stage/$name" "$pending/next-$name"
done
stopping=1
systemctl stop "${stop_units[@]}" >/dev/null 2>&1
cp -a -- "$config" "$pending/telemt.toml"
cp -a -- "$root/telemt" "$root/node-runtime" "$pending/"
if [ "$repair" = 1 ]; then
    cp -a -- "$root/agent.toml" "$system/$agent" "$pending/"
    if [ "$new_socket" = 0 ]; then cp -a -- "$root/service-control" "$pending/"; fi
    install -m 0500 "$stage/service-control" "$pending/next-service-control"
    install -m 0600 "$stage/agent.toml" "$pending/next-agent.toml"
    install -m 0644 "$stage/$agent" "$pending/next-$agent"
fi
sync -f "$pending"
snapshot=1
for name in "${selected[@]}"; do
    mv -Tf -- "$pending/next-$name" "$root/$name"
done
if [ "$repair" = 1 ]; then
    if [ "$new_socket" = 1 ]; then
        socket_published=1
        for unit in "$socket" "$helper"; do
            install -m 0644 "$stage/$unit" "$pending/next-$unit"
            replace "$pending/next-$unit" "$system/$unit"
        done
    fi
    replace "$pending/next-service-control" "$root/service-control"
    replace "$pending/next-agent.toml" "$root/agent.toml"
    replace "$pending/next-$agent" "$system/$agent"
    systemctl daemon-reload >/dev/null 2>&1
    if [ "$new_socket" = 1 ]; then
        for unit in "$socket" "$helper"; do
            [ "$(systemctl show "$(unit_ref "$unit")" -p FragmentPath --value)" = "$system/$unit" ] &&
                [ -z "$(systemctl show "$(unit_ref "$unit")" -p DropInPaths --value)" ] || exit 1
        done
        socket_enabled=1
        systemctl enable --now "$socket" >/dev/null 2>&1
    fi
fi
systemctl start "${start_units[@]}" >/dev/null 2>&1
units_active
printf 'ready\n'
read -r -t 90 decision
case "$decision" in
    commit)
        units_active
        cmp -s "$pending/telemt.toml" "$config"
        committed=1
        ;;
    abort) aborted=1 ;;
    *) exit 1 ;;
esac
