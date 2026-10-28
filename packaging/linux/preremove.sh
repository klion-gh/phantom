#!/bin/sh
# Before removal - but not before an upgrade, which postinstall handles:
# Debian passes "upgrade", RPM a remaining-instance count of 1 or more, and
# Arch runs this only on real removal.
case "${1:-}" in
	upgrade|1|2) exit 0 ;;
esac
if [ -d /run/systemd/system ]; then
	systemctl disable --now phantomd.service >/dev/null 2>&1 || true
fi
pkill -TERM -x phantomd 2>/dev/null || true
exit 0
