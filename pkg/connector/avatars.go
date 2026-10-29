package connector

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

const (
	redditorBatchSize = 50
	avatarRetryDelay  = 5 * time.Minute
)

var avatarHTTPClient = &http.Client{Timeout: 30 * time.Second}

// Reddit's chat Matrix server returns no avatars, so they come from the web
// session. A missing icon is cached as empty so it isn't requested on every
// resync, and failed lookups back off rather than repeat on every sync batch.
func (r *RedditClient) redditorAvatars(ctx context.Context, userIDs []networkid.UserID) map[networkid.UserID]*bridgev2.Avatar {
	if r.session == nil {
		return nil
	}
	if missing := r.claimUncachedRedditors(userIDs); len(missing) > 0 {
		r.fetchRedditorIcons(ctx, missing)
	}
	r.avatarMu.Lock()
	defer r.avatarMu.Unlock()
	avatars := make(map[networkid.UserID]*bridgev2.Avatar, len(userIDs))
	for _, userID := range userIDs {
		if url := r.avatarURLs[userID]; url != "" {
			avatars[userID] = &bridgev2.Avatar{
				ID:  networkid.AvatarID(url),
				Get: func(ctx context.Context) ([]byte, error) { return downloadRedditorAvatar(ctx, url) },
			}
		}
	}
	return avatars
}

// The lookup itself runs without avatarMu, which the sync path takes. Claimed
// IDs are skipped by concurrent callers until it finishes.
func (r *RedditClient) claimUncachedRedditors(userIDs []networkid.UserID) []string {
	r.avatarMu.Lock()
	defer r.avatarMu.Unlock()
	if time.Now().Before(r.avatarRetryAt) {
		return nil
	}
	if r.avatarURLs == nil {
		r.avatarURLs = make(map[networkid.UserID]string)
		r.avatarFetching = make(map[networkid.UserID]bool)
	}
	var missing []string
	for _, userID := range userIDs {
		if len(missing) == redditorBatchSize {
			break
		}
		if _, cached := r.avatarURLs[userID]; !cached && !r.avatarFetching[userID] && isRedditUserID(userID) {
			r.avatarFetching[userID] = true
			missing = append(missing, string(userID))
		}
	}
	return missing
}

func (r *RedditClient) fetchRedditorIcons(ctx context.Context, redditorIDs []string) {
	icons, err := r.session.GetRedditorIcons(ctx, redditorIDs)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to fetch Reddit avatars")
	}
	r.avatarMu.Lock()
	defer r.avatarMu.Unlock()
	if err != nil {
		r.avatarRetryAt = time.Now().Add(avatarRetryDelay)
	}
	for _, redditorID := range redditorIDs {
		delete(r.avatarFetching, networkid.UserID(redditorID))
		if err == nil {
			r.avatarURLs[networkid.UserID(redditorID)] = icons[redditorID]
		}
	}
}

func (r *RedditClient) addMemberAvatars(ctx context.Context, info *bridgev2.ChatInfo) *bridgev2.ChatInfo {
	if info.Members == nil {
		return info
	}
	userIDs := make([]networkid.UserID, 0, len(info.Members.MemberMap))
	for _, member := range info.Members.MemberMap {
		userIDs = append(userIDs, member.Sender)
	}
	avatars := r.redditorAvatars(ctx, userIDs)
	for _, member := range info.Members.MemberMap {
		if avatar := avatars[member.Sender]; avatar != nil && member.UserInfo != nil {
			member.UserInfo.Avatar = avatar
		}
	}
	return info
}

func downloadRedditorAvatar(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := avatarHTTPClient.Do(req)
	if err != nil {
		return nil, mediaError(ctx, "Reddit avatar download", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("reddit avatar is unavailable (HTTP %d)", resp.StatusCode)
	}
	return readMediaBounded(resp.Body, 2*1024*1024)
}
