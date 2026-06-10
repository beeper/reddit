package connector

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// runSync is the long-poll loop that translates Reddit-Matrix sync responses
// into bridgev2 RemoteEvents. It exits when the context is cancelled.
func (r *RedditClient) runSync(ctx context.Context) {
	defer func() {
		if r.syncDone != nil {
			close(r.syncDone)
		}
	}()
	log := zerolog.Ctx(ctx)

	timeoutMS := r.main.Config.PollTimeoutMS()
	backoff := time.Duration(r.main.Config.ErrorBackoffSeconds()) * time.Second

	for {
		if err := ctx.Err(); err != nil {
			return
		}
		resp, err := r.rc.Sync(ctx, r.meta.NextBatch, timeoutMS)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			// 401 means the chat token expired. Try to refresh once.
			if isUnauthorized(err) {
				if refreshErr := r.refreshChatToken(ctx); refreshErr != nil {
					log.Error().Err(refreshErr).Msg("Failed to refresh Reddit chat token")
					r.userLogin.BridgeState.Send(status.BridgeState{
						StateEvent: status.StateBadCredentials,
						Error:      "reddit-token-refresh-failed",
						Message:    refreshErr.Error(),
					})
					return
				}
				continue
			}
			log.Warn().Err(err).Msg("Sync failed, backing off")
			r.userLogin.BridgeState.Send(status.BridgeState{
				StateEvent: status.StateTransientDisconnect,
				Error:      "reddit-sync-error",
				Message:    err.Error(),
			})
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			continue
		}

		// First successful sync after (re)connect: mark connected.
		if r.userLogin.BridgeState.GetPrevUnsent().StateEvent != status.StateConnected {
			r.userLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
		}

		r.handleSync(ctx, resp)
		r.meta.NextBatch = resp.NextBatch
		r.saveMeta(ctx)
	}
}

func (r *RedditClient) handleSync(ctx context.Context, resp *mautrix.RespSync) {
	for roomID, joined := range resp.Rooms.Join {
		r.handleJoinedRoom(ctx, roomID, joined)
	}
	for roomID, invited := range resp.Rooms.Invite {
		r.handleInvitedRoom(ctx, roomID, invited)
	}
	for roomID := range resp.Rooms.Leave {
		r.handleLeftRoom(ctx, roomID)
	}
}

func (r *RedditClient) handleJoinedRoom(ctx context.Context, roomID id.RoomID, joined *mautrix.SyncJoinedRoom) {
	isDM := r.detectIsDM(joined.State.Events, joined.Timeline.Events)
	portalKey := r.makePortalKey(roomID, isDM)

	// Emit a ChatResync per room: bridgev2 deduplicates so we can do this every
	// sync without spamming. The GetChatInfoFunc closure resolves room name,
	// members, etc. lazily from the Reddit Matrix homeserver.
	r.userLogin.QueueRemoteEvent(&simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventChatResync,
			PortalKey:    portalKey,
			CreatePortal: true,
		},
		GetChatInfoFunc: func(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
			return r.fetchChatInfo(ctx, roomID, isDM, *joined)
		},
	})

	for _, evt := range joined.Timeline.Events {
		r.handleTimelineEvent(ctx, portalKey, evt)
	}
}

func (r *RedditClient) handleInvitedRoom(ctx context.Context, roomID id.RoomID, invited *mautrix.SyncInvitedRoom) {
	// For now we auto-accept invites by joining and letting the next sync
	// surface the room as joined. Reddit DMs arrive as invites.
	if _, err := r.matrix().JoinRoomByID(ctx, roomID); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Stringer("room_id", roomID).Msg("Failed to auto-accept Reddit invite")
	}
}

func (r *RedditClient) handleLeftRoom(ctx context.Context, roomID id.RoomID) {
	r.userLogin.QueueRemoteEvent(&simplevent.ChatDelete{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatDelete,
			PortalKey: networkid.PortalKey{ID: makePortalID(roomID), Receiver: r.userLogin.ID},
		},
		OnlyForMe: true,
	})
}

func (r *RedditClient) handleTimelineEvent(ctx context.Context, portalKey networkid.PortalKey, evt *event.Event) {
	if evt == nil {
		return
	}
	switch evt.Type {
	case event.EventMessage:
		r.queueMessage(ctx, portalKey, evt)
	case event.EventRedaction:
		r.queueRedaction(portalKey, evt)
	case event.EventReaction:
		r.queueReaction(portalKey, evt)
	default:
		// Membership, name, avatar changes etc. are picked up by the next
		// ChatResync's GetChatInfoFunc; no per-event handling needed for the
		// MVP. Typing/receipts come through ephemeral events, not timeline.
	}
}

func (r *RedditClient) queueMessage(ctx context.Context, portalKey networkid.PortalKey, evt *event.Event) {
	sender := r.makeSender(evt.Sender)
	r.userLogin.QueueRemoteEvent(&simplevent.Message[*event.Event]{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventMessage,
			PortalKey:    portalKey,
			Sender:       sender,
			CreatePortal: true,
			Timestamp:    time.UnixMilli(evt.Timestamp),
		},
		ID:                 makeMessageID(evt.ID),
		Data:               evt,
		ConvertMessageFunc: r.convertMessage,
	})
}

func (r *RedditClient) queueRedaction(portalKey networkid.PortalKey, evt *event.Event) {
	target := id.EventID(evt.Redacts)
	if target == "" {
		if content, ok := evt.Content.Parsed.(*event.RedactionEventContent); ok {
			target = content.Redacts
		}
	}
	if target == "" {
		return
	}
	r.userLogin.QueueRemoteEvent(&simplevent.MessageRemove{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventMessageRemove,
			PortalKey: portalKey,
			Sender:    r.makeSender(evt.Sender),
			Timestamp: time.UnixMilli(evt.Timestamp),
		},
		TargetMessage: makeMessageID(target),
	})
}

func (r *RedditClient) queueReaction(portalKey networkid.PortalKey, evt *event.Event) {
	content, ok := evt.Content.Parsed.(*event.ReactionEventContent)
	if !ok || content.RelatesTo.EventID == "" {
		return
	}
	r.userLogin.QueueRemoteEvent(&simplevent.Reaction{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventReaction,
			PortalKey: portalKey,
			Sender:    r.makeSender(evt.Sender),
			Timestamp: time.UnixMilli(evt.Timestamp),
		},
		EmojiID:       networkid.EmojiID(content.RelatesTo.Key),
		Emoji:         content.RelatesTo.Key,
		TargetMessage: makeMessageID(content.RelatesTo.EventID),
	})
}

// detectIsDM inspects state and a few timeline events for the m.room.create
// preset or the is_direct flag. Reddit DM rooms are created with preset
// "reddit_dm" — that's stored in m.room.create's content.
func (r *RedditClient) detectIsDM(state []*event.Event, timeline []*event.Event) bool {
	check := func(evts []*event.Event) (bool, bool) {
		for _, e := range evts {
			if e.Type != event.StateCreate {
				continue
			}
			raw, _ := e.Content.Raw["preset"].(string)
			if strings.EqualFold(raw, redditDMPreset) {
				return true, true
			}
			if v, ok := e.Content.Raw["m.federate"].(bool); ok && !v {
				// Reddit chat rooms don't federate; not a reliable signal.
				_ = v
			}
			return false, true
		}
		return false, false
	}
	if v, ok := check(state); ok {
		return v
	}
	if v, ok := check(timeline); ok {
		return v
	}
	return false
}

const redditDMPreset = "reddit_dm"

// isUnauthorized detects M_UNKNOWN_TOKEN responses so the sync loop can
// trigger a chat-token refresh.
func isUnauthorized(err error) bool {
	var httpErr mautrix.HTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	if httpErr.Response != nil && httpErr.Response.StatusCode == 401 {
		return true
	}
	if httpErr.RespError != nil {
		return httpErr.RespError.ErrCode == "M_UNKNOWN_TOKEN" || httpErr.RespError.ErrCode == "M_MISSING_TOKEN"
	}
	return false
}
