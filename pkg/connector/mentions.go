package connector

import (
	"fmt"
	"html"
	"regexp"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

var mentionPattern = regexp.MustCompile(`(?:^|[^\w@/])(@[\w-]{3,20})`)

// Reddit sends mentions as plain "@username" text with no Matrix mention data.
func (r *RedditClient) convertMentions(portal *bridgev2.Portal, content *event.MessageEventContent) {
	meta, _ := portal.Metadata.(*PortalMetadata)
	if content.Format != "" || meta == nil || meta.State == nil {
		return
	}
	formatted, mentioned := linkMentions(content.Body, meta.State.Members, r.beeperMXID)
	if len(mentioned) == 0 {
		return
	}
	content.Format = event.FormatHTML
	content.FormattedBody = formatted
	content.Mentions = &event.Mentions{UserIDs: mentioned}
}

func (r *RedditClient) beeperMXID(redditMXID id.UserID) id.UserID {
	if userID := makeUserID(redditMXID); userID != r.userID {
		return r.main.Bridge.Matrix.FormatGhostMXID(userID)
	}
	return r.userLogin.UserMXID
}

func linkMentions(body string, members map[id.UserID]RoomMember, beeperMXID func(id.UserID) id.UserID) (string, []id.UserID) {
	usernames := make(map[string]id.UserID, len(members))
	for mxid, member := range members {
		if member.Name != "" {
			usernames[strings.ToLower(stripUserPrefix(member.Name))] = mxid
		}
	}
	var formatted strings.Builder
	var mentioned []id.UserID
	last := 0
	for _, match := range mentionPattern.FindAllStringSubmatchIndex(body, -1) {
		start, end := match[2], match[3]
		redditMXID, ok := usernames[strings.ToLower(body[start+1:end])]
		if !ok {
			continue
		}
		target := beeperMXID(redditMXID)
		formatted.WriteString(plainToHTML(body[last:start]))
		_, _ = fmt.Fprintf(&formatted, `<a href="%s">%s</a>`, target.URI().MatrixToURL(), html.EscapeString(body[start:end]))
		mentioned = append(mentioned, target)
		last = end
	}
	formatted.WriteString(plainToHTML(body[last:]))
	return formatted.String(), mentioned
}

func plainToHTML(text string) string {
	return strings.ReplaceAll(html.EscapeString(text), "\n", "<br>")
}
