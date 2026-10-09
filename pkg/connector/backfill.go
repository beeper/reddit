package connector

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
)

var _ bridgev2.BackfillingNetworkAPI = (*RedditClient)(nil)
var _ bridgev2.BackfillingNetworkAPIWithLimits = (*RedditClient)(nil)

func (r *RedditClient) GetBackfillMaxBatchCount(ctx context.Context, portal *bridgev2.Portal, task *database.BackfillTask) int {
	if portal.RoomType == database.RoomTypeDM {
		return r.main.Bridge.Config.Backfill.Queue.GetOverride("dm")
	}
	return r.main.Bridge.Config.Backfill.Queue.GetOverride("group_dm")
}

// FetchMessages uses Reddit's provider cursor, not timestamps or the number of
// returned events. Short and state-only pages are not terminal: the observed
// provider terminal response has no end cursor.
func (r *RedditClient) FetchMessages(ctx context.Context, params bridgev2.FetchMessagesParams) (*bridgev2.FetchMessagesResponse, error) {
	if params.Portal == nil {
		return nil, errors.New("history requires a portal")
	}
	if params.Portal.Receiver != "" && params.Portal.Receiver != r.userLogin.ID {
		return nil, errors.New("history portal belongs to another login")
	}
	count := params.Count
	if count <= 0 {
		count = 100
	}
	count = min(count, 100)
	cursor := string(params.Cursor)
	if params.Forward {
		cursor = ""
	}
	seen := make(map[string]struct{})
	result := &bridgev2.FetchMessagesResponse{Forward: params.Forward, AggressiveDeduplication: true}
	anchor := params.AnchorMessage
	// The framework supplies the root as the initial thread anchor, but the
	// native child stream contains only replies, never the root itself.
	if params.ThreadRoot != "" && anchor != nil && anchor.ID == params.ThreadRoot {
		anchor = nil
	}
	anchorFound := false
	// A new backward scan starts at the provider's latest event, while the SDK
	// asks for events older than its oldest mapping. Walk to that exact identity
	// before converting anything, including relations on the newer side. Returning
	// a page containing only already-imported messages makes the SDK reserve the
	// task for an hour instead of continuing to older history.
	seekBackwardAnchor := !params.Forward && cursor == "" && anchor != nil
	for {
		var page *mautrix.RespMessages
		var err error
		if params.ThreadRoot != "" {
			page, err = r.remote().ThreadMessages(ctx, portalIDToRoomID(params.Portal.ID), messageIDToEventID(params.ThreadRoot), cursor, count)
		} else {
			page, err = r.remote().Messages(ctx, portalIDToRoomID(params.Portal.ID), cursor, "", count)
		}
		if err != nil {
			return nil, fmt.Errorf("fetch Reddit history: %w", err)
		}
		if cursor != "" && page.Start != "" && page.Start != cursor {
			return nil, errors.New("reddit history response does not match the requested cursor")
		}
		if page.End != "" {
			if page.End == cursor {
				return nil, errors.New("reddit history cursor did not advance")
			}
			if _, exists := seen[page.End]; exists {
				return nil, errors.New("reddit history cursor cycle")
			}
			seen[page.End] = struct{}{}
		}
		for _, evt := range page.Chunk {
			if evt == nil {
				return nil, errors.New("reddit history contains a null event")
			}
			if evt.RoomID != "" && evt.RoomID != portalIDToRoomID(params.Portal.ID) {
				return nil, errors.New("reddit history event belongs to another room")
			}
			if anchor != nil && makeMessageID(evt.ID) == anchor.ID {
				anchorFound = true
				if params.Forward {
					break
				}
				continue
			}
			if evt.StateKey != nil {
				continue
			}
			if seekBackwardAnchor && !anchorFound {
				continue
			}
			// FetchMessages returns messages and their current reactions. Live
			// removals use remote events; history must not send Matrix events or
			// mutate bridgev2's mappings as a side effect of fetching a page.
			if evt.Type != event.EventMessage {
				continue
			}
			if evt.ID == "" || evt.Sender == "" {
				return nil, errors.New("reddit history event has no stable identity")
			}
			if err = evt.Content.ParseRaw(event.EventMessage); err != nil && !errors.Is(err, event.ErrContentAlreadyParsed) {
				result.Messages = append(result.Messages, r.historyPlaceholder(ctx, evt, err))
				continue
			}
			if evt.Content.AsMessage().RelatesTo.GetReplaceID() != "" {
				continue
			}
			if params.ThreadRoot != "" && evt.Unsigned.RedactedBecause == nil && makeMessageID(evt.Content.AsMessage().RelatesTo.GetThreadParent()) != params.ThreadRoot {
				return nil, errors.New("reddit thread history contains a reply to another root")
			}
			var intent bridgev2.MatrixAPI
			if evt.Unsigned.RedactedBecause == nil && evt.Content.AsMessage().MsgType == event.MsgImage {
				intent, _ = params.Portal.GetIntentFor(ctx, r.makeSender(evt.Sender), r.userLogin, bridgev2.RemoteEventMessage)
			}
			converted, err := r.convertMessage(ctx, params.Portal, intent, evt)
			if isUnbridgeable(err) {
				result.Messages = append(result.Messages, r.historyPlaceholder(ctx, evt, err))
				continue
			} else if err != nil {
				return nil, err
			}
			var reactions []*bridgev2.BackfillReaction
			if evt.Unsigned.RedactedBecause == nil {
				reactions, err = r.backfillReactions(ctx, params.Portal.PortalKey, makeMessageID(evt.ID))
				if isUnbridgeable(err) {
					zerolog.Ctx(ctx).Warn().Err(err).Stringer("event_id", evt.ID).Msg("Skipping unbridgeable Reddit history reactions")
				} else if err != nil {
					return nil, err
				}
			}
			result.Messages = append(result.Messages, &bridgev2.BackfillMessage{ConvertedMessage: converted, ID: makeMessageID(evt.ID), Sender: r.makeSender(evt.Sender), Timestamp: time.UnixMilli(evt.Timestamp), Reactions: reactions})
		}
		result.Cursor = networkid.PaginationCursor(page.End)
		result.HasMore = page.End != ""
		if (!params.Forward && !seekBackwardAnchor) || anchor == nil || anchorFound || page.End == "" {
			break
		}
		cursor = page.End
	}
	if (params.Forward || seekBackwardAnchor) && anchor != nil && !anchorFound {
		return nil, errors.New("reddit history ended before the known message; cannot locate the history anchor")
	}
	sort.SliceStable(result.Messages, func(i, j int) bool {
		left, right := result.Messages[i], result.Messages[j]
		if left.Timestamp.Equal(right.Timestamp) {
			return left.ID < right.ID
		}
		return left.Timestamp.Before(right.Timestamp)
	})
	return result, nil
}

// Map the placeholder to the native ID so replies and reactions to it resolve.
func (r *RedditClient) historyPlaceholder(ctx context.Context, evt *event.Event, err error) *bridgev2.BackfillMessage {
	zerolog.Ctx(ctx).Warn().Err(err).Stringer("event_id", evt.ID).Msg("Failed to convert Reddit history message, using placeholder")
	return &bridgev2.BackfillMessage{
		ConvertedMessage: &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{{
			Type:    event.EventMessage,
			Content: &event.MessageEventContent{MsgType: event.MsgNotice, Body: "An error occurred while processing an incoming message", Mentions: &event.Mentions{}},
			Extra:   map[string]any{"fi.mau.bridge.internal_error": err.Error()},
		}}},
		ID: makeMessageID(evt.ID), Sender: r.makeSender(evt.Sender), Timestamp: time.UnixMilli(evt.Timestamp),
	}
}
