# Reddit Chat Go

Go wrapper for Reddit's Matrix-backed chat service using mautrix-go.

Note: the GitHub project is `github.com/mautrix/go`, but its `go.mod` declares the canonical Go import path as `maunium.net/go/mautrix`. Importing `github.com/mautrix/go` directly fails module path validation, so this library uses the canonical mautrix-go import path.

## Auth

Reddit chat stores Matrix credentials in `https://www.reddit.com` local storage after the chat app loads. The same credentials can also be minted directly from a logged-in Reddit web session by POSTing to `/svc/shreddit/token` with the `csrf_token` cookie.

Supported extraction paths:

- `LoginReddit(ctx, opts)` implements Reddit's current web login flow and returns Reddit session cookies plus Matrix credentials.
- `NewFromRedditLogin(ctx, opts)` logs in, mints Matrix credentials, and returns a ready chat client.
- `CredentialsFromRedditSession(ctx, session)` POSTs `/svc/shreddit/token`, decodes the chat JWT into a Matrix user ID, calls Matrix `whoami` for the device ID, and returns `Credentials`.
- `RedditSessionFromPlaywrightStorageState(path)` imports Reddit session cookies from a Playwright storage-state JSON file without using the old chat localStorage credentials.
- `CredentialsFromPlaywrightStorageState(path)` reads a Playwright storage-state JSON file.
- `CredentialsFromChromeDebugURL(ctx, url)` attaches to a live Chrome/Chromium debug endpoint, opens `https://www.reddit.com/chat/`, and reads the same local storage keys through CDP.
- `SeedChromeFromPlaywrightStorageState(ctx, url, path)` seeds a live Chrome/Chromium debug endpoint from a Playwright storage-state JSON file, then the CDP extractor can read from the real browser session.

Chrome debug endpoints may be passed as `http://127.0.0.1:9222` or a browser websocket URL.

Login API notes:

- Reddit serves a deterministic JavaScript verification page before the login UI; the library follows that page transition.
- The username/password step is `POST /svc/shreddit/account/login/check_is_oidc_required`, then `POST /svc/shreddit/account/login`.
- Accounts with app-based 2FA receive HTTP 202 from the password step; the OTP step is `POST /svc/shreddit/account/login/otp`.
- Reddit requires a reCAPTCHA Enterprise token for password and OTP submits. The library accepts a `CaptchaTokenProvider` callback and does not include CAPTCHA solving, media extraction, or bypass logic.
- Reddit's current login flow does not POST an image/audio CAPTCHA answer to Reddit. Google reCAPTCHA runs in the browser and returns an opaque, short-lived `recaptcha_token`; that token is the value Reddit receives.
- If no provider is configured, `LoginReddit` returns `CaptchaRequiredError` with the site key, action, and page URL needed by a caller-managed CAPTCHA flow.
- `StaticCaptchaTokenProvider(tokens...)` can be used when the caller already has one or more fresh reCAPTCHA tokens. Accounts with 2FA need two tokens: one for the password step and one for the OTP step.
- `CaptchaTokenProviderFromChromeDebugURL(url)` uses a real Chrome/Chromium debug session, opens the Reddit login page, runs Google reCAPTCHA Enterprise on Reddit's origin, and returns the resulting token. If Google presents an interactive challenge, the user handles it in that browser.
- `CaptchaRequest.Step` is `password` or `otp`, so UI code can show why a token is being requested.
- For an embedded WebView provider, load `CaptchaRequest.PageURL`, then evaluate `CaptchaRequest.EnterpriseExecuteJavaScript()` on that page and return the resulting token. Username, password, and 2FA do not need to be entered in the WebView.
- TOTP generation is built in through `GenerateTOTP` and `RedditLoginOptions.TOTPSecret`.

Minimal login shape:

```go
client, session, err := redditchat.NewFromRedditLogin(ctx, redditchat.RedditLoginOptions{
    Username:   username,
    Password:   password,
    TOTPSecret: totpSecret,
    CaptchaTokenProvider: redditchat.CaptchaTokenProviderFromChromeDebugURL("http://127.0.0.1:9222"),
})
_ = client
_ = session
```

The Chrome-backed provider needs a running browser with remote debugging enabled, for example:

```sh
google-chrome --remote-debugging-port=9222 --user-data-dir=/tmp/reddit-login-captcha
```

The important detail is origin: the Reddit reCAPTCHA site key must be executed from `https://www.reddit.com/login/`. A local HTML page or unrelated WebView origin will usually produce an invalid token.

WebView provider shape:

```go
CaptchaTokenProvider: func(ctx context.Context, req redditchat.CaptchaRequest) (string, error) {
    webview.Navigate(req.PageURL)
    token, err := webview.EvaluateJavaScript(ctx, req.EnterpriseExecuteJavaScript())
    if err != nil {
        return "", err
    }
    return token, nil
}
```

The WebView is only for reCAPTCHA. `LoginReddit` still submits username, password, and TOTP through the reversed Reddit HTTP endpoints.

## API Surface

The package exposes mautrix-backed helpers for:

- `LoginReddit`, `NewFromRedditLogin`, `CredentialsFromRedditSession`, `RedditSessionFromPlaywrightStorageState`
- `CaptchaTokenProviderFromChromeDebugURL`, `CaptchaTokenFromChromeDebugURL`, `StaticCaptchaTokenProvider`
- `Whoami`, `Capabilities`, `Sync`, `Messages`, `GetEvent`, `JoinedRooms`
- `SearchUsers`, `RoomsWithUser`, `DirectRoomsWithUser`
- `CreateDM`, `CreateOrGetDM`, `CreateGroup`, `Invite`, `AcceptInvite`, `Leave`
- `SendText`, `SendNotice`, `SendMessage`
- `SetTyping`, `MarkRead`
- `MediaConfig`, `UploadMedia`, `SendFile`, `SendImage`, `SendVideo`, `SendAudio`, `SendMedia`
- `SendMXCFile`, `SendMXCImage`, `SendMXCVideo`, `SendMXCAudio`, `SendMXCMedia`
- `DownloadMedia`, `DownloadMediaBytes`

Raw mautrix access is available through `Client.Mautrix()`.

## Smoke Command

Authenticate both saved test sessions and sync:

```sh
go run ./cmd/redditchat-smoke
```

Derive Matrix credentials from Reddit session cookies instead of chat localStorage:

```sh
go run ./cmd/redditchat-smoke -session-auth
```

Use Chrome debug protocol instead of saved storage state:

```sh
go run ./cmd/redditchat-smoke -cdp-a http://127.0.0.1:9222 -cdp-b http://127.0.0.1:9223
```

Seed real Chrome debug sessions from the saved test states, then authenticate through CDP:

```sh
go run ./cmd/redditchat-smoke \
  -seed-cdp \
  -cdp-a http://127.0.0.1:9222 \
  -cdp-b http://127.0.0.1:9223
```

Send a text message through the verified DM room:

```sh
go run ./cmd/redditchat-smoke \
  -room '!7qgWTmcqCdRXuLbvlk3ggseaFXdE5jltGuMrhpSbq6o:reddit.com' \
  -message 'hello from mautrix'
```

Get or create a DM, or create a group room, then send the smoke message:

```sh
go run ./cmd/redditchat-smoke -create-dm
go run ./cmd/redditchat-smoke -create-group
```

Send media through the Matrix media API:

```sh
go run ./cmd/redditchat-smoke \
  -room '!7qgWTmcqCdRXuLbvlk3ggseaFXdE5jltGuMrhpSbq6o:reddit.com' \
  -image /tmp/reddit-chat-smoke.png
```

Send an already-hosted Reddit MXC media event:

```sh
go run ./cmd/redditchat-smoke \
  -room '!7qgWTmcqCdRXuLbvlk3ggseaFXdE5jltGuMrhpSbq6o:reddit.com' \
  -image-url 'mxc://reddit.com/<media-id>' \
  -media-name image.jpg \
  -media-content-type image/jpeg
```

Print Reddit Matrix media limits:

```sh
go run ./cmd/redditchat-smoke -media-config
```

## Current Live Evidence

- Both test accounts authenticate via Reddit's Matrix tokens.
- Reddit login capture reversed the current web endpoints: password login uses `/svc/shreddit/account/login`; 2FA uses `/svc/shreddit/account/login/otp`; chat token refresh uses `POST /svc/shreddit/token`.
- `-session-auth` was verified with a post-login state that had no chat localStorage bootstrap: account A minted fresh Matrix credentials as `@t2_2foi1of47y:reddit.com` device `63491a13e9d7e54e91096051125ff818`, and account B also minted fresh credentials from Reddit session cookies. Both synced successfully.
- Both test accounts now have verified email addresses in Reddit settings.
- `Sync` works for both accounts.
- Reddit's web client uses `preset: "reddit_dm"` for direct chats and `preset: "private_chat"` for group rooms. The wrapper now matches those presets.
- The web client checks `/_matrix/client/v3/rooms?with_user=...&type=direct&include=state,timeline` before creating a DM. `CreateOrGetDM` implements the same reuse flow.
- `CreateDM` worked previously with a minimal mautrix `CreateRoom` request; repeated smoke runs now reuse the existing DM to avoid room-creation quota.
- Account B accepted the invite via `JoinRoomByID`.
- Account A sent a text message via mautrix in the verified DM room and account B observed it via `Sync`.
- Real Chromium CDP sessions were launched on ports 9222 and 9223, seeded from the saved states, and used for auth/sync through `CredentialsFromChromeDebugURL`.
- CDP-backed send/receive worked in room `!7qgWTmcqCdRXuLbvlk3ggseaFXdE5jltGuMrhpSbq6o:reddit.com`; sent event `$bY18t2ZghWQqGKKUy3K2nn52UcwTlatxDjPiNaTi6lA` was observed by account B.
- The latest `CreateOrGetDM` smoke reused that room and sent event `$9Pie9l3KG8pohOoV_vQayz-7nHoVt058QYW5DnjSm1s`, observed by account B.
- `SendMXCImage` with a `mxc://reddit.com/...` URI sent event `$KpRv0U9TGl_0hYbM9Rd9xHSzOXBvzD4Q-95o80OqZMU`, observed by account B.

New-device note: a message send using a freshly minted session-auth Matrix device reached the existing DM room but Reddit returned `M_FORBIDDEN: User is flagged for spam`. The same accounts can still sync, and earlier established devices can send in the DM. This appears to be another Reddit account/device trust decision rather than a missing Matrix API call.

Group note: `CreateGroup` is implemented as a non-direct Matrix room creation with invites. Live creation is currently blocked by Reddit rate limits/account-establishment gates on the test accounts:

- After email verification and chat reload, account A receives `M_LIMIT_EXCEEDED: Limit exceeded for number of invites attempted` with `rate.score_invitation_limit`.
- Earlier runs also hit `rate.score_room_creation_limit` on account A and `rate.score_invitation_limit_ln` when account B was used as the creator.

Media note: Reddit currently returns `M_FORBIDDEN: Media upload forbidden` for standard Matrix media upload from these sessions, even after both accounts were email verified and the chat app was reloaded. `MediaConfig` reports `m.upload.size: 20971520` and `com.reddit.upload.size: {"image/gif":104857600}`, so the endpoint is present. A headed browser capture of the real Reddit chat UI selected `/tmp/reddit-chat-smoke.png` and the UI itself posted to `https://matrix.redditspace.com/_matrix/media/v3/upload?filename=reddit-chat-smoke.png`, the same endpoint mautrix uses. Reddit returned HTTP 403 and displayed "An error occurred while uploading the image. Please try again." Reddit also rejects HTTPS media URLs and foreign MXC origins in message content, but accepts `mxc://reddit.com/...` media events. A synthetic Reddit-origin MXC URI sends and renders as an image event; a fake media ID will not download actual bytes.
