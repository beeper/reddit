# Reddit Chat for Beeper

A self-hosted Beeper bridge for Reddit chat. It runs locally, signs in with
your Reddit account, and brings Reddit chat conversations into Beeper.

This is not an official Reddit API integration. Reddit web changes may
require bridge updates.

## What Works

- Reddit login through Beeper Desktop (username/password, app-based 2FA).
- Direct messages and group chats, including starting new ones from Beeper.
- Text and media send and receive.
- Replies, edits, reactions, read receipts, and typing notifications.

## Configuration

Direct media uses the standard top-level `direct_media` configuration: enable
it with a reachable media server name/delegation and a persistent server signing
key. Keep that name and key stable so existing signed URLs remain usable. The
bridge must retain the originating Reddit login and Reddit must still serve the
file. Downloads use bounded memory and sniff the actual image MIME type; they
are proxied on demand, without a second media cache or an upload to Matrix.
Reddit can transcode WebP to JPEG, so native event metadata may differ from the
bytes served by the proxy. Existing reuploaded messages are unchanged.

Ordinary Matrix leave events also require `bridge.bridge_matrix_leave: true`;
Beeper membership state requests require `bridge.enable_send_state_requests: true`.
The existing Delete/Ignore action remains available independently of
the Matrix-leave switch. DMs are hidden for the owner, while groups are left.
Group invite/kick capabilities remain rejected in DMs; Reddit enforces group
permissions and invitation limits. Ban/unban and role changes are unsupported.

## Setup

Install and log in to [`bbctl`](https://github.com/beeper/bridge-manager):

```sh
bbctl login
```

Build in the source checkout (Go 1.26+ and libolm are required):

```sh
./build.sh
```

Install libolm with `brew install libolm` on macOS or
`apt-get install libolm-dev` on Debian/Ubuntu.
Generate credentials in a private runtime directory outside the source
checkout. Use a dedicated bridge name for the first registration:

```sh
install -d -m 700 "$HOME/.local/share/beeper-reddit"
cd "$HOME/.local/share/beeper-reddit"
umask 077
bbctl config --type bridgev2 --param pickle_key=generate -o "$PWD/config.yaml" sh-reddit
bbctl register -g -o "$PWD/registration.yaml" sh-reddit
```

Enable Beeper's state-request path in `config.yaml` so group-name changes can
reach the connector. Set this field under the `bridge` section:

```yaml
bridge:
    enable_send_state_requests: true
```

Then start the bridge:

```sh
/absolute/path/to/reddit/reddit -c config.yaml -r registration.yaml
```

For updates, stop the bridge process and replace its binary while retaining
the same runtime, registration, database and encryption key.

Then open Beeper Desktop, go to Settings -> Bridges -> Self-hosted Bridges,
find `sh-reddit`, add an account, and complete the Reddit login flow.

## Troubleshooting

Check the bridge is registered and connected:

```sh
bbctl whoami | grep reddit
```

If messages are not syncing, confirm the bridge shows a connected remote and
check the bridge logs for recent errors.

Do not share `config.yaml`, `registration.yaml`, the database, or logs
publicly — they may contain account or connection details.

## Project Layout

```text
cmd/reddit/        Bridge binary entrypoint
pkg/connector/     Beeper bridge connector
pkg/redditchat/    Reddit chat client library
```
