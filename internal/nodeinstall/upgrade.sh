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
[ "$(id -u)" = 0 ] && [ "$(uname -m)" = "$arch" ] || exit 11
exec 8>/run/telemt-node-install.lock
flock -n 8 || exit 14
[ ! -e "$pending" ] && [ ! -L "$pending" ] || exit 25
[ "$(stat -c '%u:%a' "$root")" = 0:755 ] && [ ! -L "$root" ] || exit 11
[ -f "$config" ] && [ ! -L "$config" ] && [ "$(readlink -f "$config")" = "$config" ] || exit 11
check_unit() {
    local unit=$1 expected=$2
    [ "$(systemctl show "$unit" -p FragmentPath --value)" = "/etc/systemd/system/$unit" ] &&
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
[ "${#selected[@]}" != 0 ] || exit 26
[ ! -e "$backup" ] && [ ! -L "$backup" ] || exit 11
mkdir -m 0700 -- "$pending"

stopping=0
snapshot=0
committed=0
aborted=0
units_active() {
    systemctl is-active --quiet "$proxy" && systemctl is-active --quiet "$agent" &&
        systemctl is-active --quiet "$gateway"
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
            else
                restored=0
            fi
        fi
        # A crashing candidate can consume the unit's systemd start budget.
        systemctl reset-failed "${start_units[@]}" >/dev/null 2>&1 || true
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
sync -f "$pending"
snapshot=1
for name in "${selected[@]}"; do
    mv -Tf -- "$pending/next-$name" "$root/$name"
done
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
