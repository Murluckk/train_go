#!/usr/bin/env bash
#
# Installs drill on this machine as a systemd service bound to loopback.
# Run it on the server, as root, from a checkout of the repository:
#
#   ./deploy/install.sh
#
# Re-running it is how you deploy a new version.

set -euo pipefail

PREFIX=/opt/drill
SERVICE=drill
USER_NAME=drill

log() { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

[[ $EUID -eq 0 ]] || die "run as root"
[[ -f go.mod && -d cmd/drill ]] || die "run this from the repository root"
command -v systemctl >/dev/null || die "systemd is required"

# The runner shells out to the go toolchain for every submission, so the
# server needs a real Go installation, not just the built binary.
GO_BIN=$(command -v go || true)
[[ -n $GO_BIN ]] || GO_BIN=$([[ -x /usr/local/go/bin/go ]] && echo /usr/local/go/bin/go || true)
[[ -n $GO_BIN ]] || die "the go toolchain is not installed; drill needs it to run submissions
  install it with:  curl -sSL https://go.dev/dl/go1.26.0.linux-amd64.tar.gz | tar -C /usr/local -xz"

GO_VERSION=$("$GO_BIN" env GOVERSION)
log "toolchain: $GO_BIN ($GO_VERSION)"

log "building"
"$GO_BIN" build -trimpath -o /tmp/drill.new ./cmd/drill

if ! id -u "$USER_NAME" >/dev/null 2>&1; then
    log "creating the $USER_NAME service account"
    useradd --system --shell /usr/sbin/nologin --home-dir /var/lib/drill "$USER_NAME"
fi

log "installing into $PREFIX"
install -d -o root -g root -m 0755 "$PREFIX"
install -o root -g root -m 0755 /tmp/drill.new "$PREFIX/drill"
rm -f /tmp/drill.new

# Tasks are data, not code the service writes to, so root owns them and the
# service only reads them. Edit them over ssh and rerun this script, or just
# rsync the directory: drill rescans it every two seconds.
rm -rf "$PREFIX/tasks.new"
cp -r tasks "$PREFIX/tasks.new"
rm -rf "$PREFIX/tasks.old"
[[ -d $PREFIX/tasks ]] && mv "$PREFIX/tasks" "$PREFIX/tasks.old"
mv "$PREFIX/tasks.new" "$PREFIX/tasks"
rm -rf "$PREFIX/tasks.old"
chown -R root:root "$PREFIX/tasks"

install -d -o "$USER_NAME" -g "$USER_NAME" -m 0750 /var/lib/drill /var/cache/drill

log "installing the unit"
install -o root -g root -m 0644 deploy/drill.service /etc/systemd/system/drill.service
sed -i "s|^Environment=PATH=.*|Environment=PATH=$(dirname "$GO_BIN"):/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin|" \
    /etc/systemd/system/drill.service

systemctl daemon-reload
systemctl enable --now "$SERVICE"

log "waiting for the service to come up"
for _ in $(seq 1 60); do
    if curl -fsS --max-time 2 http://127.0.0.1:8080/readyz >/dev/null 2>&1; then
        break
    fi
    sleep 1
done

if ! curl -fsS --max-time 5 http://127.0.0.1:8080/readyz; then
    echo
    die "the service did not become ready; see: journalctl -u $SERVICE -n 50"
fi
echo

cat <<'DONE'

==> installed

The service listens on 127.0.0.1:8080 only. It has no authentication and it
compiles and runs whatever is submitted to it, so do not put it on a public
interface. Reach it from your own machine with a tunnel:

    ssh -N -L 8080:127.0.0.1:8080 root@<server>

and open http://127.0.0.1:8080

    systemctl status drill
    journalctl -u drill -f
DONE
