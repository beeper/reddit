package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/id"

	"github.com/beeper/reddit/pkg/redditchat"
)

type RedditClient struct {
	main      *RedditConnector
	userLogin *bridgev2.UserLogin
	userID    networkid.UserID

	rc       *redditchat.Client
	clientMu sync.RWMutex
	session  *redditchat.RedditSession
	meta     *UserLoginMetadata

	syncMu     sync.Mutex
	syncCancel context.CancelFunc
	syncDone   chan struct{}

	saveMu sync.Mutex
	roomMu sync.RWMutex
	rooms  map[id.RoomID]*RoomState
}

var _ bridgev2.NetworkAPI = (*RedditClient)(nil)

func NewRedditClient(rc *RedditConnector, login *bridgev2.UserLogin) (*RedditClient, error) {
	meta, ok := login.Metadata.(*UserLoginMetadata)
	if !ok || meta == nil {
		return nil, fmt.Errorf("unexpected login metadata type %T", login.Metadata)
	}
	mxClient, err := redditchat.New(meta.Credentials)
	if err != nil {
		return nil, err
	}
	session, err := restoreSession(meta)
	if err != nil {
		// A missing session jar is non-fatal — the user can still chat, they
		// just can't refresh the chat token without re-login. Log and continue.
		session = nil
	}
	return &RedditClient{
		main:      rc,
		userLogin: login,
		userID:    makeUserID(id.UserID(meta.Credentials.UserID)),
		rc:        mxClient,
		session:   session,
		meta:      meta,
	}, nil
}

func restoreSession(meta *UserLoginMetadata) (*redditchat.RedditSession, error) {
	if meta.CookiesJSON == "" {
		return nil, errors.New("no cookies in metadata")
	}
	var cookies []*http.Cookie
	if err := json.Unmarshal([]byte(meta.CookiesJSON), &cookies); err != nil {
		return nil, err
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	httpClient := &http.Client{Jar: jar}
	session, err := redditchat.NewRedditSession(httpClient, "", "")
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(session.BaseURL)
	if err != nil {
		return nil, err
	}
	jar.SetCookies(u, cookies)
	return session, nil
}

func (r *RedditClient) Connect(ctx context.Context) {
	if !r.IsLoggedIn() {
		r.userLogin.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateBadCredentials,
			Error:      "reddit-no-auth",
			Message:    "Reddit credentials missing",
		})
		return
	}
	r.syncMu.Lock()
	defer r.syncMu.Unlock()
	if r.syncCancel != nil {
		return
	}
	r.userLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnecting})
	syncCtx, cancel := context.WithCancel(r.userLogin.Log.WithContext(context.Background()))
	r.syncCancel = cancel
	r.syncDone = make(chan struct{})
	go r.runSync(syncCtx, r.syncDone)
}

func (r *RedditClient) Disconnect() {
	r.syncMu.Lock()
	cancel := r.syncCancel
	done := r.syncDone
	r.syncMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
}

func (r *RedditClient) IsLoggedIn() bool {
	client := r.remote()
	return client != nil && client.Mautrix().AccessToken != ""
}

func (r *RedditClient) IsThisUser(ctx context.Context, userID networkid.UserID) bool {
	return r.userID == userID
}

func (r *RedditClient) LogoutRemote(ctx context.Context) {
	if !r.IsLoggedIn() {
		return
	}
	if _, err := r.remote().Mautrix().Logout(ctx); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to log out of Reddit Matrix homeserver")
	}
}

// saveMeta persists the UserLoginMetadata. Callers should mutate r.meta first,
// then call this method. Concurrent callers are serialized.
func (r *RedditClient) saveMeta(ctx context.Context) error {
	r.saveMu.Lock()
	defer r.saveMu.Unlock()
	if err := r.userLogin.Save(ctx); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Msg("Failed to save user login metadata")
		return err
	}
	return nil
}

var errRefreshAccountMismatch = errors.New("reddit session belongs to a different account; sign in again with the original account")

// refreshChatToken re-mints the Reddit Matrix access token using the saved
// Reddit session cookies. Called on 401 from the Matrix homeserver.
func (r *RedditClient) refreshChatToken(ctx context.Context) error {
	if r.session == nil {
		return redditchat.ErrSessionUnavailable
	}
	token, creds, err := redditchat.CredentialsFromRedditSession(ctx, r.session)
	if err != nil {
		return err
	}
	if creds.UserID != r.meta.Credentials.UserID || makeUserLoginID(id.UserID(creds.UserID)) != r.userLogin.ID {
		return errRefreshAccountMismatch
	}
	if creds.DeviceID == "" {
		creds.DeviceID = r.meta.Credentials.DeviceID
	}
	newClient, err := redditchat.New(creds)
	if err != nil {
		return err
	}
	cookies, err := serializeCookies(r.session)
	if err != nil {
		return fmt.Errorf("serialize refreshed Reddit session: %w", err)
	}
	previousCreds, previousExpiry, previousCookies := r.meta.Credentials, r.meta.ChatTokenExpiry, r.meta.CookiesJSON
	r.meta.Credentials, r.meta.ChatTokenExpiry, r.meta.CookiesJSON = creds, token.Expires, cookies
	if err = r.saveMeta(ctx); err != nil {
		r.meta.Credentials, r.meta.ChatTokenExpiry, r.meta.CookiesJSON = previousCreds, previousExpiry, previousCookies
		return fmt.Errorf("save refreshed Reddit session: %w", err)
	}
	r.clientMu.Lock()
	r.rc = newClient
	r.clientMu.Unlock()
	return nil
}

func refreshNeedsLogin(err error) bool {
	var httpErr *redditchat.LoginHTTPError
	return errors.Is(err, redditchat.ErrSessionUnavailable) || errors.Is(err, errRefreshAccountMismatch) ||
		(errors.As(err, &httpErr) && (httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden))
}

func (r *RedditClient) matrix() *mautrix.Client {
	return r.remote().Mautrix()
}

// A refreshed client is published as one immutable object. In-flight requests
// may finish on the old token; later requests always see the new client.
func (r *RedditClient) remote() *redditchat.Client {
	r.clientMu.RLock()
	defer r.clientMu.RUnlock()
	return r.rc
}
