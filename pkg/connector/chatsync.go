package connector

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/beeper/reddit/pkg/redditchat"
)

// runSync is the long-poll loop that translates Reddit-Matrix sync responses
// into bridgev2 RemoteEvents. It exits when the context is cancelled.
func (r *RedditClient) runSync(ctx context.Context, done chan struct{}) {
	defer func() {
		r.syncMu.Lock()
		if r.syncDone == done {
			r.syncCancel = nil
			r.syncDone = nil
		}
		close(done)
		r.syncMu.Unlock()
	}()
	log := zerolog.Ctx(ctx)

	timeoutMS := r.main.Config.PollTimeoutMS()
	backoff := time.Duration(r.main.Config.ErrorBackoffSeconds()) * time.Second

	for {
		if err := ctx.Err(); err != nil {
			return
		}
		since := r.meta.NextBatch
		if r.meta.RoomStateVersion < 4 {
			since = ""
		}
		resp, err := r.remote().Sync(ctx, since, timeoutMS)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			// An expired chat token may still have a renewable Reddit session.
			if isUnauthorized(err) {
				refreshErr := r.refreshChatToken(ctx)
				if refreshErr == nil {
					continue
				}
				if ctx.Err() != nil {
					return
				}
				if refreshNeedsLogin(refreshErr) {
					log.Warn().Err(refreshErr).Msg("Reddit session requires reauthentication")
					r.userLogin.BridgeState.Send(status.BridgeState{
						StateEvent: status.StateBadCredentials,
						Error:      "reddit-token-refresh-failed",
						Message:    refreshErr.Error(),
						UserAction: status.UserActionRelogin,
					})
					return
				}
				// Provider outages, rate limits and local save failures retain the
				// existing session and retry through the normal native poll loop.
				err = refreshErr
			}
			log.Warn().Err(err).Msg("Sync failed, backing off")
			r.userLogin.BridgeState.Send(status.BridgeState{
				StateEvent: status.StateTransientDisconnect,
				Error:      "reddit-sync-error",
				Message:    "Unable to synchronize Reddit chats. Retrying automatically.",
			})
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			continue
		}

		err = r.handleSync(ctx, resp, &roomSnapshots{})
		if err != nil {
			log.Error().Err(err).Msg("Failed to process Reddit sync; retaining cursor")
			r.userLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateTransientDisconnect, Error: "reddit-sync-apply-failed", Message: "Unable to synchronize Reddit chat state. Retrying."})
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			continue
		}
		// All framework event calls have returned. Advance the native token;
		// event delivery and crash safety belong to bridgev2 and its runtime.
		previousCursor, previousVersion := r.meta.NextBatch, r.meta.RoomStateVersion
		r.meta.NextBatch, r.meta.RoomStateVersion = resp.NextBatch, 4
		if err = r.saveMeta(ctx); err != nil {
			r.meta.NextBatch, r.meta.RoomStateVersion = previousCursor, previousVersion
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			continue
		}
		// First successfully processed sync after (re)connect: mark connected.
		if r.userLogin.BridgeState.GetPrevUnsent().StateEvent != status.StateConnected {
			r.userLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
		}

	}
}

// Unbridgeable native data is skipped so it cannot hold the cursor forever.
// Any other failure retries the whole batch.
func (r *RedditClient) handleSync(ctx context.Context, resp *redditchat.SyncResponse, snapshots *roomSnapshots) error {
	unhidden, err := r.applyHiddenChatData(ctx, resp)
	if err != nil {
		return err
	}
	for roomID, joined := range resp.Rooms.Join {
		if r.meta.HiddenRooms[roomID] {
			continue
		}
		if err = skipUnbridgeable(ctx, r.handleRoomUpdate(ctx, roomID, joined, snapshots), roomID, ""); err != nil {
			return err
		}
	}
	for roomID, invited := range resp.Rooms.Invite {
		if r.meta.HiddenRooms[roomID] {
			continue
		}
		if err = skipUnbridgeable(ctx, r.handleInvitedRoom(ctx, roomID, invited), roomID, ""); err != nil {
			return err
		}
	}
	// Pending requests receive subsequent messages in rooms.peek. Process
	// invitation state first, then use the same queue/backfill path as joined
	// timelines. Membership comes from state, never from the response bucket.
	for roomID, peek := range resp.Rooms.Peek {
		if peek == nil || r.meta.HiddenRooms[roomID] || resp.Rooms.Join[roomID] != nil || resp.Rooms.Leave[roomID] != nil {
			continue
		}
		if len(peek.State.Events)+len(peek.Timeline.Events)+len(peek.Ephemeral.Events) == 0 {
			continue
		}
		if err = skipUnbridgeable(ctx, r.handleRoomUpdate(ctx, roomID, peek, snapshots), roomID, ""); err != nil {
			return err
		}
	}
	for roomID := range resp.Rooms.Leave {
		if err = r.handleLeftRoom(ctx, roomID); err != nil {
			return err
		}
	}
	if len(unhidden) > 0 {
		state, err := r.fetchRoomSnapshots(ctx, snapshots, unhidden)
		if err != nil {
			return err
		}
		return r.handleSync(ctx, state, snapshots)
	}
	return nil
}

func skipUnbridgeable(ctx context.Context, err error, roomID id.RoomID, eventID id.EventID) error {
	if !isUnbridgeable(err) {
		return err
	}
	zerolog.Ctx(ctx).Warn().Err(err).Stringer("room_id", roomID).Stringer("event_id", eventID).Msg("Skipping unbridgeable Reddit data")
	return nil
}

func (r *RedditClient) applyHiddenChatData(ctx context.Context, resp *redditchat.SyncResponse) ([]id.RoomID, error) {
	var unhidden []id.RoomID
	// Apply account data before invitations: full native sync includes hidden
	// DMs in both peek and invite. Never mistake a preview for membership.
	for _, rooms := range []map[id.RoomID]*mautrix.SyncJoinedRoom{resp.Rooms.Join, resp.Rooms.Peek} {
		for roomID, room := range rooms {
			if room == nil {
				continue
			}
			for _, evt := range room.AccountData.Events {
				if evt == nil || evt.Type.Type != "com.reddit.hidden_chat" {
					continue
				}
				hidden, err := parseHiddenChat(evt)
				if err != nil {
					zerolog.Ctx(ctx).Warn().Err(err).Stringer("room_id", roomID).Stringer("event_id", evt.ID).Msg("Skipping unbridgeable Reddit hidden chat state")
					continue
				}
				if hidden {
					if r.meta.HiddenRooms == nil {
						r.meta.HiddenRooms = make(map[id.RoomID]bool)
					}
					r.meta.HiddenRooms[roomID] = true
					if err = r.handleLeftRoom(ctx, roomID); err != nil {
						return nil, err
					}
				} else {
					delete(r.meta.HiddenRooms, roomID)
					if resp.Rooms.Join[roomID] == nil && resp.Rooms.Invite[roomID] == nil {
						unhidden = append(unhidden, roomID)
					}
				}
			}
		}
	}
	return unhidden, nil
}

func parseHiddenChat(evt *event.Event) (bool, error) {
	var content struct {
		Hidden *bool `json:"hidden"`
	}
	raw, err := json.Marshal(&evt.Content)
	if err != nil {
		return false, err
	}
	if err = json.Unmarshal(raw, &content); err != nil {
		return false, err
	}
	if content.Hidden == nil {
		return false, errors.New("reddit hidden chat state has no hidden flag")
	}
	return *content.Hidden, nil
}

func (r *RedditClient) handleRoomUpdate(ctx context.Context, roomID id.RoomID, joined *mautrix.SyncJoinedRoom, snapshots *roomSnapshots) error {
	if joined == nil {
		return unbridgeable(errors.New("reddit sync contains a null room"))
	}
	state, portalKey, err := r.resolveRoomState(ctx, roomID, joined, snapshots)
	if err != nil {
		return err
	}

	resync := r.joinedRoomEvent(state, portalKey, joined.Timeline.Limited)
	r.userLogin.QueueRemoteEvent(resync)

	for _, evt := range joined.Timeline.Events {
		if evt == nil {
			continue
		}
		if err = skipUnbridgeable(ctx, r.handleTimelineEvent(ctx, portalKey, evt), roomID, evt.ID); err != nil {
			return err
		}
	}
	return nil
}

func (r *RedditClient) handleInvitedRoom(ctx context.Context, roomID id.RoomID, invited *mautrix.SyncInvitedRoom) error {
	joined, err := r.inviteSnapshot(invited)
	if err != nil {
		return unbridgeable(err)
	}
	state, key, err := r.roomState(ctx, roomID, joined)
	if err != nil {
		return err
	}
	request := r.pendingRequestEvent(state, key)
	// Receiving an invitation must never join the remote room. Only an
	// explicit acceptance or a user reply may cross that boundary.
	r.userLogin.QueueRemoteEvent(request)
	return nil
}

func (r *RedditClient) handleLeftRoom(ctx context.Context, roomID id.RoomID) error {
	portal, err := r.savedRoomPortal(ctx, roomID)
	if err != nil || portal == nil {
		return err
	}
	deletion := &simplevent.ChatDelete{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatDelete,
			PortalKey: portal.PortalKey,
		},
		OnlyForMe: true,
	}
	r.userLogin.QueueRemoteEvent(deletion)
	return nil
}

func (r *RedditClient) handleTimelineEvent(ctx context.Context, portalKey networkid.PortalKey, evt *event.Event) error {
	if evt.Type == event.EventMessage || evt.Type == event.EventReaction || evt.Type == event.EventRedaction {
		if evt.ID == "" || evt.Sender == "" || (evt.RoomID != "" && makePortalID(evt.RoomID) != portalKey.ID) {
			return unbridgeable(errors.New("reddit timeline event has invalid room or event identity"))
		}
		if err := evt.Content.ParseRaw(evt.Type); err != nil && !errors.Is(err, event.ErrContentAlreadyParsed) {
			return unbridgeable(err)
		}
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
		// ChatResync. Typing/receipts come through ephemeral events.
	}
	return nil
}

// Let bridgev2 choose the catch-up anchor and run FetchMessages, as Slack does.
func (r *RedditClient) joinedRoomEvent(state *RoomState, key networkid.PortalKey, limited bool) *simplevent.ChatResync {
	info := r.chatInfoFromState(state)
	return &simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{Type: bridgev2.RemoteEventChatResync, PortalKey: key, CreatePortal: true},
		ChatInfo:  info,
		CheckNeedsBackfillFunc: func(_ context.Context, latest *database.Message) (bool, error) {
			return info.CanBackfill && (limited || latest == nil), nil
		},
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
