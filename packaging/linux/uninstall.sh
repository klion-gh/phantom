#!/bin/sh
# Removes a Phantom installed by install.sh (packages are removed with the
# package manager instead). Saved configs in ~/.config/Phantom are kept.
#
#   sudo sh /usr/lib/phantom/uninstall.sh
set -eu

if [ "$(id -u)" != 0 ]; then
	echo "Run as root: sudo sh $0" >&2
	exit 1
fi

if [ -d /run/systemd/system ]; then
	systemctl disable --now phantomd.service >/dev/null 2>&1 || true
	rm -f /etc/systemd/system/phantomd.service
	systemctl daemon-reload
fi
# Without systemd, a phantomd started through pkexec may still be running;
# SIGTERM makes it restore normal networking before it exits.
pkill -TERM -x phantomd 2>/dev/null || true

rm -f /usr/bin/phantom \
	/usr/share/applications/phantom.desktop \
	/usr/share/icons/hicolor/256x256/apps/phantom.png \
	/usr/share/polkit-1/actions/com.phantom.vpn.policy
rm -rf /usr/lib/phantom

echo "Phantom removed (configs in ~/.config/Phantom were kept)"
