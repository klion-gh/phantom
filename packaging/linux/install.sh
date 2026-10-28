#!/bin/sh
# Installs Phantom from the universal phantom-linux-<arch>.tar.gz - for any
# distribution without a .deb/.rpm/.pkg.tar.zst of its own. Run from the
# unpacked directory:
#
#   sudo sh install.sh
#
# Also what the window's updater runs for an install made this way. Installs:
#   /usr/bin/phantom                     the window
#   /usr/lib/phantom/phantomd            the service that owns the tunnel
#   /etc/systemd/system/phantomd.service (where systemd exists; started now)
#   desktop entry, icon, polkit policy, uninstall.sh
set -eu

if [ "$(id -u)" != 0 ]; then
	echo "Run as root: sudo sh $0" >&2
	exit 1
fi

here=$(cd "$(dirname "$0")" && pwd)

# Copy to a temporary name and rename over the target: replacing the files of
# a running window or service in place fails with "text file busy".
put() { # mode source target
	install -D -m "$1" "$here/$2" "$3.new"
	mv -f "$3.new" "$3"
}

put 755 phantom /usr/bin/phantom
put 755 phantomd /usr/lib/phantom/phantomd
put 755 uninstall.sh /usr/lib/phantom/uninstall.sh
put 644 phantom.desktop /usr/share/applications/phantom.desktop
put 644 phantom.png /usr/share/icons/hicolor/256x256/apps/phantom.png
put 644 com.phantom.vpn.policy /usr/share/polkit-1/actions/com.phantom.vpn.policy
echo tar > /usr/lib/phantom/install-method

if [ -d /run/systemd/system ]; then
	put 644 phantomd.service /etc/systemd/system/phantomd.service
	systemctl daemon-reload
	systemctl enable phantomd.service >/dev/null 2>&1
	systemctl restart phantomd.service
	echo "phantomd service installed and started"
else
	echo "No systemd: the window starts phantomd itself (asking for the password once per session)"
fi

command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database -q /usr/share/applications || true
command -v gtk-update-icon-cache >/dev/null 2>&1 && gtk-update-icon-cache -q -t /usr/share/icons/hicolor || true

# The window needs GTK 3 and WebKitGTK 4.1 from the distribution. Say which
# package provides them rather than leaving the user with a linker error.
if command -v ldd >/dev/null 2>&1 && ldd /usr/bin/phantom 2>/dev/null | grep -q "not found"; then
	echo
	echo "Missing libraries for the window:"
	ldd /usr/bin/phantom | grep "not found" | sed 's/^/  /'
	. /etc/os-release 2>/dev/null || true
	case " ${ID:-} ${ID_LIKE:-} " in
		*" debian "*|*" ubuntu "*) echo "Install them with: sudo apt install libgtk-3-0 libwebkit2gtk-4.1-0" ;;
		*" fedora "*|*" rhel "*) echo "Install them with: sudo dnf install gtk3 webkit2gtk4.1" ;;
		*" suse "*|*" opensuse "*) echo "Install them with: sudo zypper install libgtk-3-0 libwebkit2gtk-4_1-0" ;;
		*" arch "*) echo "Install them with: sudo pacman -S gtk3 webkit2gtk-4.1" ;;
		*) echo "Install GTK 3 and WebKitGTK 4.1 with your distribution's package manager." ;;
	esac
fi

echo "Phantom installed - start it from the application menu or run: phantom"
