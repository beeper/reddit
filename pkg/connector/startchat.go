package connector

import (
	"context"
	"errors"
	"strings"

	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

var (
	_ bridgev2.IdentifierResolvingNetworkAPI = (*RedditClient)(nil)
	_ bridgev2.UserSearchingNetworkAPI       = (*RedditClient)(nil)
	_ bridgev2.GroupCreatingNetworkAPI       = (*RedditClient)(nil)
)

// ResolveIdentifier turns a Reddit username (with or without "u/" prefix) or
// t2_ user ID into a ghost + optional DM. Usernames are looked up in Reddit's
// user directory and IDs by profile; if createChat is set, it then opens (or
// finds) a DM with the resolved user.
func (r *RedditClient) ResolveIdentifier(ctx context.Context, identifier string, createChat bool) (*bridgev2.ResolveIdentifierResponse, error) {
	identifier = strings.TrimSpace(identifier)
	identifier = strings.TrimPrefix(identifier, "u/")
	identifier = strings.TrimPrefix(identifier, "/u/")
	if identifier == "" {
		return nil, bridgev2.RespError(mautrix.MInvalidParam.WithMessage("Enter a Reddit username."))
	}
	var match *userDirectoryHit
	if userID := networkid.UserID(identifier); isRedditUserID(userID) {
		mxid := userIDToMatrix(userID, "")
		profile, err := r.matrix().GetProfile(ctx, mxid)
		if err == nil {
			name := profile.DisplayName
			if name == "" {
				name = identifier
			}
			match = &userDirectoryHit{UserID: mxid, Name: name}
		} else if !errors.Is(err, mautrix.MNotFound) {
			return nil, wrapRedditRespError(err)
		}
	} else {
		results, err := r.remote().SearchUsers(ctx, identifier)
		if err != nil {
			return nil, wrapRedditRespError(err)
		}
		for _, hit := range results.Results {
			display := hit.DisplayName
			if strings.EqualFold(stripUserPrefix(display), identifier) || strings.EqualFold(string(hit.UserID), "@"+identifier+":reddit.com") {
				if match != nil && match.UserID != hit.UserID {
					return nil, bridgev2.RespError(mautrix.MInvalidParam.WithMessage("Multiple Reddit users match %q.", identifier))
				}
				match = &userDirectoryHit{UserID: hit.UserID, Name: display, Avatar: string(hit.AvatarURL)}
			}
		}
	}
	if match == nil {
		return nil, bridgev2.RespError(mautrix.MNotFound.WithMessage("No Reddit user found for %q.", identifier))
	}

	uid := makeUserID(match.UserID)
	resp := &bridgev2.ResolveIdentifierResponse{
		UserID: uid,
		UserInfo: &bridgev2.UserInfo{
			Name: ptr.Ptr(r.main.Config.FormatDisplayname(DisplaynameParams{
				Username: stripUserPrefix(match.Name),
				UserID:   string(uid),
			})),
		},
	}
	if !createChat {
		return resp, nil
	}
	roomID, created, err := r.remote().CreateOrGetDM(ctx, match.UserID)
	if err != nil {
		return nil, wrapRedditRespError(err)
	}
	portalKey := networkid.PortalKey{ID: makePortalID(roomID), Receiver: r.userLogin.ID}
	resp.Chat = &bridgev2.CreateChatResponse{
		PortalKey: portalKey,
		PortalInfo: &bridgev2.ChatInfo{
			Type: ptr.Ptr(database.RoomTypeDM),
		},
	}
	if created {
		members := &bridgev2.ChatMemberList{MemberMap: make(bridgev2.ChatMemberMap), OtherUserID: uid}
		members.MemberMap.Set(bridgev2.ChatMember{EventSender: r.makeSender(userIDToMatrix(r.userID, "")), Membership: event.MembershipJoin})
		members.MemberMap.Set(bridgev2.ChatMember{EventSender: r.makeSender(match.UserID), Membership: event.MembershipInvite})
		resp.Chat.PortalInfo.Name = resp.UserInfo.Name
		resp.Chat.PortalInfo.Members = members
		resp.Chat.PortalInfo.CanBackfill = true
	}
	return resp, nil
}

// SearchUsers returns multiple matches for a query.
func (r *RedditClient) SearchUsers(ctx context.Context, query string) ([]*bridgev2.ResolveIdentifierResponse, error) {
	query = strings.TrimSpace(query)
	results, err := r.remote().SearchUsers(ctx, query)
	if err != nil {
		return nil, wrapRedditRespError(err)
	}
	out := make([]*bridgev2.ResolveIdentifierResponse, 0, len(results.Results))
	for _, hit := range results.Results {
		uid := makeUserID(hit.UserID)
		out = append(out, &bridgev2.ResolveIdentifierResponse{
			UserID: uid,
			UserInfo: &bridgev2.UserInfo{
				Name: ptr.Ptr(r.main.Config.FormatDisplayname(DisplaynameParams{
					Username: stripUserPrefix(hit.DisplayName),
					UserID:   string(uid),
				})),
			},
		})
	}
	return out, nil
}

// CreateGroup spins up a private Reddit chat with the requested participants.
// Reddit has rate limits on group creation for new accounts.
func (r *RedditClient) CreateGroup(ctx context.Context, params *bridgev2.GroupCreateParams) (*bridgev2.CreateChatResponse, error) {
	if params == nil || params.Name == nil || params.Name.Name == "" {
		return nil, bridgev2.RespError(mautrix.MInvalidParam.WithMessage("Group name is required."))
	}
	if len(params.Participants) < 2 {
		return nil, bridgev2.RespError(mautrix.MInvalidParam.WithMessage("At least 2 participants are required."))
	}
	mxids := make([]id.UserID, 0, len(params.Participants))
	for _, uid := range params.Participants {
		mxids = append(mxids, userIDToMatrix(uid, ""))
	}
	roomID, err := r.remote().CreateGroup(ctx, params.Name.Name, mxids)
	if err != nil {
		return nil, wrapRedditRespError(err)
	}
	portalKey := networkid.PortalKey{ID: makePortalID(roomID)}
	if r.main.Bridge.Config.SplitPortals {
		portalKey.Receiver = r.userLogin.ID
	}
	return &bridgev2.CreateChatResponse{
		PortalKey: portalKey,
		PortalInfo: &bridgev2.ChatInfo{
			Name: ptr.Ptr(params.Name.Name),
			Type: ptr.Ptr(database.RoomTypeDefault),
		},
	}, nil
}

type userDirectoryHit struct {
	UserID id.UserID
	Name   string
	Avatar string
}
