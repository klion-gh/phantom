#!/bin/sh
# After install and after every upgrade: (re)start the service so the new
# phantomd is the one running. A tunnel that was up drops here; the window
# reconnects on the user's next click, as after any restart.
if [ -d /run/systemd/system ]; then
	systemctl daemon-reload || true
	systemctl enable phantomd.service >/dev/null 2>&1 || true
	systemctl restart phantomd.service || true
fi
command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database -q /usr/share/applications || true
command -v gtk-update-icon-cache >/dev/null 2>&1 && gtk-update-icon-cache -q -t /usr/share/icons/hicolor || true
exit 0
