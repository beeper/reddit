package connector

import (
	"context"
	"strings"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/mediaproxy"
)

var _ bridgev2.DirectMediableNetwork = (*RedditConnector)(nil)

func (rc *RedditConnector) SetUseDirectMedia() { rc.directMedia = true }

// Store only the account and stable native MXC identity. Bridgev2 signs this
// opaque ID; CDN URLs, access tokens and attachment keys never go into it.
func (r *RedditClient) directMediaURI(ctx context.Context, uri id.ContentURIString) (id.ContentURIString, error) {
	parsed, err := uri.Parse()
	if err != nil || parsed.Homeserver != "reddit.com" || parsed.FileID == "" || strings.ContainsRune(parsed.FileID, 0) {
		return "", mediaproxy.ErrInvalidMediaIDSyntax
	}
	return r.main.Bridge.Matrix.GenerateContentURI(ctx, networkid.MediaID("1\x00"+string(r.userLogin.ID)+"\x00"+parsed.FileID))
}

func (rc *RedditConnector) Download(ctx context.Context, mediaID networkid.MediaID, _ map[string]string) (mediaproxy.GetMediaResponse, error) {
	parts := strings.Split(string(mediaID), "\x00")
	if len(parts) != 3 || parts[0] != "1" || parts[1] == "" || parts[2] == "" {
		return nil, mediaproxy.ErrInvalidMediaIDSyntax
	}
	login, err := rc.Bridge.GetExistingUserLoginByID(ctx, networkid.UserLoginID(parts[1]))
	if err != nil {
		return nil, err
	} else if login == nil {
		return nil, mautrix.MNotFound.WithMessage("Reddit media account no longer exists")
	}
	client, ok := login.Client.(*RedditClient)
	if !ok || !client.IsLoggedIn() {
		return nil, mautrix.MNotFound.WithMessage("Reddit media account is not logged in")
	}
	// Resolve through the current login on every request, including after token
	// renewal. The existing bounded downloader follows Reddit's current redirect
	// and sanitizes errors so a signed CDN URL cannot escape through diagnostics.
	data, err := client.downloadRedditMedia(ctx, id.ContentURI{Homeserver: "reddit.com", FileID: parts[2]}.CUString(), nil)
	if err != nil {
		return nil, err
	}
	if _, err = imageMIME(data); err != nil {
		return nil, err
	}
	return mediaproxy.GetMediaResponseRawData(data), nil
}
