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

## Setup

Install and log in to [`bbctl`](https://github.com/beeper/bridge-manager):

```sh
bbctl login
```

Register the bridge and generate its config:

```sh
bbctl config --type bridgev2 -o config.yaml sh-reddit
bbctl register -g -o registration.yaml sh-reddit
```

Run the bridge (requires Go 1.26+ and libolm — `brew install libolm` on
macOS, `apt-get install libolm-dev` on Debian/Ubuntu):

```sh
go run ./cmd/mautrix-reddit -c config.yaml -r registration.yaml
```

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
cmd/mautrix-reddit/   Bridge binary entrypoint
pkg/connector/        Beeper bridge connector
pkg/redditchat/       Reddit chat client library
```
