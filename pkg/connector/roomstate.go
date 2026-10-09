package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// RoomState is the current projection of Reddit's sync state. Reddit supplies
// state events in the timeline too, and doesn't implement joined_members.
// Persist this projection so incremental syncs don't change a portal's identity.
type RoomState struct {
	Type         string                         `json:"type"`
	Name         *string                        `json:"name,omitempty"`
	Topic        *string                        `json:"topic,omitempty"`
	Members      map[id.UserID]RoomMember       `json:"members"`
	JoinedCount  *int                           `json:"joined_count,omitempty"`
	InvitedCount *int                           `json:"invited_count,omitempty"`
	PowerLevels  *event.PowerLevelsEventContent `json:"power_levels,omitempty"`
}

type RoomMember struct {
	Name       string           `json:"name,omitempty"`
	Membership event.Membership `json:"membership"`
}

var errMissingChatType = errors.New("reddit sync is missing chat type; refusing to guess a recipient")

func mergeRoomState(previous *RoomState, joined *mautrix.SyncJoinedRoom) (*RoomState, error) {
	next := &RoomState{Members: make(map[id.UserID]RoomMember)}
	if previous != nil {
		*next = *previous
		next.Members = maps.Clone(previous.Members)
		if next.Members == nil {
			next.Members = make(map[id.UserID]RoomMember)
		}
	}
	lists := [][]*event.Event{joined.State.Events, joined.Timeline.Events}
	if joined.StateAfter != nil {
		lists = append(lists, joined.StateAfter.Events)
	}
	for _, list := range lists {
		for _, evt := range list {
			if evt == nil || evt.StateKey == nil {
				continue
			}
			data, err := json.Marshal(&evt.Content)
			if err != nil {
				return nil, fmt.Errorf("encode room state: %w", err)
			}
			switch evt.Type.Type {
			case "com.reddit.chat.type":
				var content struct {
					Type string `json:"type"`
				}
				if err = json.Unmarshal(data, &content); err != nil {
					return nil, err
				}
				if content.Type != "direct" && content.Type != "private_group" {
					return nil, errors.New("unsupported Reddit chat type")
				}
				if next.Type != "" && next.Type != content.Type {
					return nil, errors.New("reddit chat type changed; portal migration required")
				}
				next.Type = content.Type
			case "m.room.member":
				var content event.MemberEventContent
				if err = json.Unmarshal(data, &content); err != nil {
					return nil, err
				}
				uid := id.UserID(*evt.StateKey)
				if _, _, err = uid.Parse(); err != nil {
					return nil, errors.New("invalid Reddit room member ID")
				}
				if content.Membership == event.MembershipJoin || content.Membership == event.MembershipInvite {
					member := next.Members[uid]
					member.Membership = content.Membership
					if content.Displayname != "" {
						member.Name = content.Displayname
					}
					next.Members[uid] = member
				} else if content.Membership == event.MembershipLeave || content.Membership == event.MembershipBan {
					delete(next.Members, uid)
				} else {
					return nil, errors.New("unsupported Reddit member state")
				}
			case "m.room.name":
				var content event.RoomNameEventContent
				if err = json.Unmarshal(data, &content); err != nil {
					return nil, err
				}
				next.Name = ptr.Ptr(content.Name)
			case "m.room.topic":
				var content event.TopicEventContent
				if err = json.Unmarshal(data, &content); err != nil {
					return nil, err
				}
				next.Topic = ptr.Ptr(content.Topic)
			case "m.room.power_levels":
				var content event.PowerLevelsEventContent
				if err = json.Unmarshal(data, &content); err != nil {
					return nil, err
				}
				next.PowerLevels = &content
			}
		}
	}
	if joined.Summary.JoinedMemberCount != nil {
		next.JoinedCount = joined.Summary.JoinedMemberCount
	}
	if joined.Summary.InvitedMemberCount != nil {
		next.InvitedCount = joined.Summary.InvitedMemberCount
	}
	return next, nil
}

func (r *RedditClient) roomState(ctx context.Context, roomID id.RoomID, joined *mautrix.SyncJoinedRoom) (*RoomState, networkid.PortalKey, error) {
	r.roomMu.Lock()
	defer r.roomMu.Unlock()
	if r.rooms == nil {
		r.rooms = make(map[id.RoomID]*RoomState)
	}
	previous := r.rooms[roomID]
	existing, err := r.savedRoomPortal(ctx, roomID)
	if err != nil {
		return nil, networkid.PortalKey{}, err
	}
	if previous == nil && existing != nil {
		if meta, ok := existing.Metadata.(*PortalMetadata); ok && meta != nil {
			previous = meta.State
		}
	}
	next, err := mergeRoomState(previous, joined)
	if err != nil {
		return nil, networkid.PortalKey{}, unbridgeable(err)
	}
	if next.Type == "" {
		return nil, networkid.PortalKey{}, unbridgeable(errMissingChatType)
	}
	key := r.makePortalKey(roomID, next.Type == "direct")
	if existing != nil && existing.PortalKey != key {
		return nil, key, unbridgeable(errors.New("saved portal ownership does not match Reddit chat type"))
	}
	r.rooms[roomID] = next
	return next, key, nil
}

// DMs and split groups belong to this login; ordinary groups have an empty
// receiver. Only the latter may fall back to a shared persisted projection.
// Reading the SDK database also makes reload independent of its portal cache.
func (r *RedditClient) savedRoomPortal(ctx context.Context, roomID id.RoomID) (*database.Portal, error) {
	key := networkid.PortalKey{ID: makePortalID(roomID), Receiver: r.userLogin.ID}
	portal, err := r.main.Bridge.DB.Portal.GetByKey(ctx, key)
	if err != nil || portal != nil || r.main.Bridge.Config.SplitPortals {
		return portal, err
	}
	key.Receiver = ""
	return r.main.Bridge.DB.Portal.GetByKey(ctx, key)
}

// A portal row can exist without saved state, e.g. after a failed resync, and
// incremental syncs omit the chat type. Fetch the snapshot outside roomMu.
func (r *RedditClient) resolveRoomState(ctx context.Context, roomID id.RoomID, joined *mautrix.SyncJoinedRoom, snapshots *roomSnapshots) (*RoomState, networkid.PortalKey, error) {
	state, key, err := r.roomState(ctx, roomID, joined)
	if !errors.Is(err, errMissingChatType) {
		return state, key, err
	}
	full, err := r.fetchRoomSnapshots(ctx, snapshots, []id.RoomID{roomID})
	if err != nil {
		return nil, networkid.PortalKey{}, err
	}
	snapshot := full.Rooms.Join[roomID]
	if snapshot == nil && full.Rooms.Invite[roomID] == nil {
		return nil, networkid.PortalKey{}, unbridgeable(errors.New("reddit did not return the requested conversation state"))
	} else if snapshot == nil {
		if snapshot, err = r.inviteSnapshot(full.Rooms.Invite[roomID]); err != nil {
			return nil, networkid.PortalKey{}, unbridgeable(err)
		}
	}
	return r.roomState(ctx, roomID, snapshot)
}

func (r *RedditClient) chatInfoFromState(state *RoomState) *bridgev2.ChatInfo {
	info := &bridgev2.ChatInfo{Name: state.Name, Topic: state.Topic, CanBackfill: true, Type: ptr.Ptr(database.RoomTypeGroupDM)}
	if own, known := state.Members[userIDToMatrix(r.userID, "")]; known {
		request := own.Membership == event.MembershipInvite
		info.MessageRequest = &request
		// Invited DM and group accounts can read /messages and reaction
		// relations without joining. Return the native preview through normal
		// backfill; the request flag and remote membership remain pending.
		info.CanBackfill = !request || state.Type == "direct" || state.Type == "private_group"
	}
	if state.Type == "direct" {
		info.Type = ptr.Ptr(database.RoomTypeDM)
	}
	members := &bridgev2.ChatMemberList{MemberMap: make(bridgev2.ChatMemberMap)}
	if state.Type == "private_group" && state.PowerLevels != nil {
		// The SDK maps native permissions and each member to its Matrix user.
		members.PowerLevels = &bridgev2.PowerLevelOverrides{
			Events: map[event.Type]int{event.StateRoomName: state.PowerLevels.GetEventLevel(event.StateRoomName)},
			Invite: ptr.Ptr(state.PowerLevels.Invite()),
			Kick:   ptr.Ptr(state.PowerLevels.Kick()),
		}
	}
	for mxid, member := range state.Members {
		name := member.Name
		if name == "" {
			name = string(makeUserID(mxid))
		}
		formatted := r.main.Config.FormatDisplayname(DisplaynameParams{Username: stripUserPrefix(name), UserID: string(makeUserID(mxid))})
		membership := member.Membership
		if mxid == userIDToMatrix(r.userID, "") && membership == event.MembershipInvite {
			// The owner must be joined on Beeper to see and accept the request.
			// Native membership remains invite in the saved projection; only the
			// explicit accept handler joins Reddit. The SDK excludes invitations
			// from a new Matrix room's initial member list.
			membership = event.MembershipJoin
		}
		mapped := bridgev2.ChatMember{EventSender: r.makeSender(mxid), Membership: membership, UserInfo: &bridgev2.UserInfo{Name: &formatted}}
		if members.PowerLevels != nil {
			mapped.PowerLevel = ptr.Ptr(state.PowerLevels.GetUserLevel(mxid))
		}
		members.MemberMap.Set(mapped)
		if state.Type == "direct" && makeUserID(mxid) != r.userID {
			members.OtherUserID = makeUserID(mxid)
			if info.Name == nil {
				info.Name = &formatted
			}
		}
	}
	members.TotalMemberCount = len(members.MemberMap)
	if state.JoinedCount != nil && state.InvitedCount != nil {
		members.TotalMemberCount = *state.JoinedCount + *state.InvitedCount
		members.IsFull = members.TotalMemberCount == len(members.MemberMap)
	}
	info.Members = members
	info.ExtraUpdates = func(ctx context.Context, portal *bridgev2.Portal) bool {
		meta, ok := portal.Metadata.(*PortalMetadata)
		if !ok || meta == nil {
			meta = &PortalMetadata{}
			portal.Metadata = meta
		}
		meta.State = state
		if state.Type == "direct" {
			meta.Preset = redditDMPreset
		} else {
			meta.Preset = "private_chat"
		}
		return true
	}
	return info
}
