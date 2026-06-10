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

	"prohibition/redditchat"
)

type RedditClient struct {
	main      *RedditConnector
	userLogin *bridgev2.UserLogin
	userID    networkid.UserID

	rc      *redditchat.Client
	session *redditchat.RedditSession
	meta    *UserLoginMetadata

	syncMu     sync.Mutex
	syncCancel context.CancelFunc
	syncDone   chan struct{}

	saveMu sync.Mutex
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
	r.userLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnecting})

	r.syncMu.Lock()
	defer r.syncMu.Unlock()
	if r.syncCancel != nil {
		return
	}
	syncCtx, cancel := context.WithCancel(r.userLogin.Log.WithContext(context.Background()))
	r.syncCancel = cancel
	r.syncDone = make(chan struct{})
	go r.runSync(syncCtx)
}

func (r *RedditClient) Disconnect() {
	r.syncMu.Lock()
	cancel := r.syncCancel
	done := r.syncDone
	r.syncCancel = nil
	r.syncDone = nil
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
	return r.rc != nil && r.meta.Credentials.AccessToken != ""
}

func (r *RedditClient) IsThisUser(ctx context.Context, userID networkid.UserID) bool {
	return r.userID == userID
}

func (r *RedditClient) LogoutRemote(ctx context.Context) {
	if !r.IsLoggedIn() {
		return
	}
	if _, err := r.rc.Mautrix().Logout(ctx); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to log out of Reddit Matrix homeserver")
	}
}

// saveMeta persists the UserLoginMetadata. Callers should mutate r.meta first,
// then call this method. Concurrent callers are serialized.
func (r *RedditClient) saveMeta(ctx context.Context) {
	r.saveMu.Lock()
	defer r.saveMu.Unlock()
	if err := r.userLogin.Save(ctx); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Msg("Failed to save user login metadata")
	}
}

// refreshChatToken re-mints the Reddit Matrix access token using the saved
// Reddit session cookies. Called on 401 from the Matrix homeserver.
func (r *RedditClient) refreshChatToken(ctx context.Context) error {
	if r.session == nil {
		return errors.New("no reddit session available for refresh")
	}
	token, creds, err := redditchat.CredentialsFromRedditSession(ctx, r.session)
	if err != nil {
		return err
	}
	creds.DeviceID = r.meta.Credentials.DeviceID
	r.meta.Credentials = creds
	r.meta.ChatTokenExpiry = token.Expires
	newClient, err := redditchat.New(creds)
	if err != nil {
		return err
	}
	r.rc = newClient
	r.saveMeta(ctx)
	return nil
}

func (r *RedditClient) matrix() *mautrix.Client {
	return r.rc.Mautrix()
}
