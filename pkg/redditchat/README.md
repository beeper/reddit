# Reddit chat protocol client

This package implements the Reddit endpoints used by the bridge. It uses
`maunium.net/go/mautrix` for Reddit's Matrix-shaped chat protocol; bridgev2 owns
local message delivery and mappings.

## Authentication

The connector drives the native operations through bridgev2 login steps:

1. Create a `RedditSession` and call `PrepareLogin` with the username.
2. Present `CaptchaRequest(CaptchaStepPassword)` in the framework's hidden
   verification webview, then pass its result to `SubmitPassword`.
3. If Reddit requires two-factor authentication, collect the user's code and
   a fresh `CaptchaRequest(CaptchaStepOTP)`, then call `SubmitOTP` on the same
   session. Do not repeat the password login.
4. Call `CredentialsFromRedditSession` to mint chat credentials. The connector
   persists the session cookies for renewal and validates the account on refresh.

Each operation accepts a context and performs only that step's HTTP requests.
There is no background login worker or browser-debug connection. OTP codes are
provided by the user; the bridge does not accept authenticator secrets.

## Chat protocol

Reddit uses `matrix.redditspace.com`, native `t2_` user IDs, opaque event IDs
and provider pagination cursors. Its request previews also appear in
`rooms.peek`. Media downloads use the native `/media/v3/download` endpoint;
thread pages use Reddit's sequenced relations API. These differences are
handled here and by the connector, without overriding bridgev2 delivery.

Local protocol tests use synthetic responses and do not establish end-to-end
acceptance.
