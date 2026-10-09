#!/bin/sh
set -eu
umask 077

bridge_uid="${UID:-1337}"
bridge_gid="${GID:-$bridge_uid}"
case "$bridge_uid:$bridge_gid" in
	*[!0-9:]*|:*|*:) echo "UID and GID must be numeric" >&2; exit 1 ;;
esac

mkdir -p /data
chmod 700 /data
cd /data

# Generate and upgrade files as the same numeric user that runs the bridge.
# The previous launcher wrote the initial 0600 config as root, preventing the
# host user from editing it even when UID/GID were explicitly supplied.
if [ "$(id -u)" = 0 ] && [ "$bridge_uid" != 0 ]; then
	chown -R "$bridge_uid:$bridge_gid" /data
	exec su-exec "$bridge_uid:$bridge_gid" "$0" "$@"
fi

if [ "$#" -gt 0 ]; then
	exec /usr/bin/reddit "$@"
fi

if [ ! -f /data/config.yaml ]; then
	/usr/bin/reddit -c /data/config.yaml -e
	echo "Didn't find a config file."
	echo "Copied default config file to /data/config.yaml"
	echo "Modify that config file to your liking."
	echo "Start the container again after that to generate the registration file."
	exit
fi

if [ ! -f /data/registration.yaml ]; then
	/usr/bin/reddit -g -c /data/config.yaml -r /data/registration.yaml || exit $?
	echo "Didn't find a registration file."
	echo "Generated one for you."
	echo "See https://docs.mau.fi/bridges/general/registering-appservices.html on how to use it."
	exit
fi

chmod 600 /data/config.yaml /data/registration.yaml
exec /usr/bin/reddit -c /data/config.yaml -r /data/registration.yaml
