package connector

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/variationselector"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/beeper/reddit/pkg/redditchat"
)

// Unicode aliases select corresponding native images. The full native palette
// is available through image-pack import, including reactions without aliases.
var reactionAliases = map[string]string{
	"😂": "joy", "👋": "wave", "😢": "cry", "😵": "dizzy_face",
	"🤦": "facepalm", "😳": "flushed", "😬": "grimacing", "😁": "grin",
	"😍": "heart_eyes", "🤗": "hug", "😘": "kissing_heart", "😆": "laughing",
	"🤑": "money_face", "😐": "neutral_face", "😶": "no_mouth", "😡": "rage",
	"😱": "scream", "🤷": "shrug", "😴": "sleep", "🙂": "slightly_smiling",
	"😄": "smile", "😭": "sob", "😛": "stuck_out_tongue", "😎": "sunglasses",
	"😮": "surprise", "😓": "sweat", "😅": "sweat_smile", "🤔": "thinking_face_hmm",
	"👎": "thumbs_down", "👍": "thumbs_up", "😉": "wink", "😋": "yummy",
}

func nativeReactionKey(value string) (string, bool) {
	name := reactionAliases[variationselector.Remove(value)]
	for _, asset := range redditchat.ReactionAssets() {
		if value == asset.Key || value == ":"+asset.Name+":" || asset.Name == name {
			return asset.Key, true
		}
	}
	return "", false
}

var reactionHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || req.URL.Scheme != "https" || req.URL.Host != "i.redd.it" {
			return errors.New("unexpected reaction image redirect")
		}
		return nil
	},
}

func (r *RedditClient) reactionImage(ctx context.Context, key string) (string, string, error) {
	asset, known := redditchat.ReactionAssetByKey(key)
	if !known {
		// Preserve unknown future keys visibly, without fetching arbitrary URLs.
		return key, "", nil
	}
	r.main.emojiMu.Lock()
	defer r.main.emojiMu.Unlock()
	mxc := r.main.Bridge.DB.KV.Get(ctx, database.Key("reddit_emoji_v1/key/"+key))
	if mxc == "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://i.redd.it/"+asset.Key, nil)
		if err != nil {
			return "", "", err
		}
		resp, err := reactionHTTPClient.Do(req)
		if err != nil {
			return "", "", mediaError(ctx, "Reddit reaction download", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("reddit reaction image is unavailable (HTTP %d)", resp.StatusCode)
			if isGoneStatus(resp.StatusCode) {
				err = unbridgeable(err)
			}
			return "", "", err
		}
		data, err := readMediaBounded(resp.Body, 2*1024*1024)
		if err != nil {
			return "", "", err
		}
		mime, err := imageMIME(data)
		if err != nil {
			return "", "", err
		}
		// Public emoji assets use unencrypted media, since a Matrix reaction key
		// cannot carry attachment decryption metadata. Private images never use this.
		uri, file, err := r.main.Bridge.Bot.UploadMedia(ctx, "", data, asset.Key, mime)
		if err != nil {
			return "", "", mediaError(ctx, "Beeper reaction upload", err)
		}
		if file != nil || uri == "" {
			return "", "", errors.New("reaction upload did not return a public media URI")
		}
		mxc = string(uri)
		r.main.Bridge.DB.KV.Set(ctx, database.Key("reddit_emoji_v1/mxc/"+mxc), key)
		r.main.Bridge.DB.KV.Set(ctx, database.Key("reddit_emoji_v1/key/"+key), mxc)
	}
	return mxc, ":" + asset.Name + ":", nil
}

// A reaction image that can never be fetched falls back to text.
func (r *RedditClient) reactionEmoji(ctx context.Context, key string) (string, string, error) {
	emoji, shortcode, err := r.reactionImage(ctx, key)
	if !isUnbridgeable(err) {
		return emoji, shortcode, err
	}
	asset, _ := redditchat.ReactionAssetByKey(key)
	zerolog.Ctx(ctx).Warn().Err(err).Str("reaction", asset.Name).Msg("Failed to fetch Reddit reaction image, using text")
	for unicode, name := range reactionAliases {
		if name == asset.Name {
			return unicode, "", nil
		}
	}
	return ":" + asset.Name + ":", "", nil
}

func (r *RedditClient) resolveOutgoingReaction(ctx context.Context, value string) (string, error) {
	if strings.HasPrefix(value, "mxc://") {
		key := r.main.Bridge.DB.KV.Get(ctx, database.Key("reddit_emoji_v1/mxc/"+value))
		if _, ok := redditchat.ReactionAssetByKey(key); ok {
			return key, nil
		}
	} else if key, ok := nativeReactionKey(value); ok {
		return key, nil
	}
	return "", errors.New("reddit supports its native reaction palette; choose an imported Reddit reaction or a matching emoji")
}

var _ bridgev2.StickerImportingNetworkAPI = (*RedditClient)(nil)

const reactionPackURL = "https://www.reddit.com/chat/"

func redditReactionPack() *event.ImagePackMetadata {
	return &event.ImagePackMetadata{DisplayName: "Reddit reactions", Usage: []event.ImagePackUsage{event.ImagePackUsageEmoji}, BridgedPack: &event.BridgedStickerPack{Network: "reddit", URL: reactionPackURL}}
}
func (r *RedditClient) ListImagePacks(ctx context.Context) ([]*event.ImagePackMetadata, error) {
	return []*event.ImagePackMetadata{redditReactionPack()}, nil
}
func (r *RedditClient) DownloadImagePack(ctx context.Context, packURL string) (*bridgev2.ImportedImagePack, error) {
	if packURL != reactionPackURL {
		return nil, errors.New("unknown Reddit reaction pack")
	}
	images := make(map[string]*event.ImagePackImage)
	for _, asset := range redditchat.ReactionAssets() {
		mxc, _, err := r.reactionImage(ctx, asset.Key)
		if err != nil {
			return nil, err
		}
		images[asset.Name] = &event.ImagePackImage{URL: id.ContentURIString(mxc)}
	}
	return &bridgev2.ImportedImagePack{Shortcode: "reddit-reactions", Content: &event.ImagePackEventContent{Images: images, Metadata: *redditReactionPack()}}, nil
}
