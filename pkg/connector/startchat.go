package connector

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"
)

var (
	_ bridgev2.IdentifierResolvingNetworkAPI = (*RedditClient)(nil)
	_ bridgev2.UserSearchingNetworkAPI       = (*RedditClient)(nil)
	_ bridgev2.GroupCreatingNetworkAPI       = (*RedditClient)(nil)
)

// ResolveIdentifier turns a Reddit username (with or without "u/" prefix) into
// a ghost + optional DM. It first searches Reddit's user directory, then —
// if createChat is set — opens (or finds) a DM with the resolved user.
func (r *RedditClient) ResolveIdentifier(ctx context.Context, identifier string, createChat bool) (*bridgev2.ResolveIdentifierResponse, error) {
	identifier = strings.TrimSpace(identifier)
	identifier = strings.TrimPrefix(identifier, "u/")
	identifier = strings.TrimPrefix(identifier, "/u/")
	if identifier == "" {
		return nil, errors.New("empty identifier")
	}
	results, err := r.rc.SearchUsers(ctx, identifier)
	if err != nil {
		return nil, fmt.Errorf("search users: %w", err)
	}
	var match *userDirectoryHit
	for _, hit := range results.Results {
		display := hit.DisplayName
		if strings.EqualFold(stripUserPrefix(display), identifier) || strings.EqualFold(string(hit.UserID), "@"+identifier+":reddit.com") {
			match = &userDirectoryHit{UserID: hit.UserID, Name: display, Avatar: string(hit.AvatarURL)}
			break
		}
	}
	if match == nil && len(results.Results) > 0 {
		first := results.Results[0]
		match = &userDirectoryHit{UserID: first.UserID, Name: first.DisplayName, Avatar: string(first.AvatarURL)}
	}
	if match == nil {
		return nil, fmt.Errorf("no Reddit user matched %q", identifier)
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
	roomID, _, err := r.rc.CreateOrGetDM(ctx, match.UserID)
	if err != nil {
		return nil, fmt.Errorf("create dm: %w", err)
	}
	portalKey := networkid.PortalKey{ID: makePortalID(roomID), Receiver: r.userLogin.ID}
	resp.Chat = &bridgev2.CreateChatResponse{
		PortalKey: portalKey,
		PortalInfo: &bridgev2.ChatInfo{
			Type: ptr.Ptr(database.RoomTypeDM),
		},
	}
	return resp, nil
}

// SearchUsers returns multiple matches for a query.
func (r *RedditClient) SearchUsers(ctx context.Context, query string) ([]*bridgev2.ResolveIdentifierResponse, error) {
	query = strings.TrimSpace(query)
	results, err := r.rc.SearchUsers(ctx, query)
	if err != nil {
		return nil, err
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
// Reddit has rate limits on group creation for new accounts; surface errors
// directly from the underlying library to the caller.
func (r *RedditClient) CreateGroup(ctx context.Context, params *bridgev2.GroupCreateParams) (*bridgev2.CreateChatResponse, error) {
	if params == nil || params.Name == nil || params.Name.Name == "" {
		return nil, errors.New("group name is required")
	}
	if len(params.Participants) < 2 {
		return nil, errors.New("at least 2 participants are required")
	}
	mxids := make([]id.UserID, 0, len(params.Participants))
	for _, uid := range params.Participants {
		mxids = append(mxids, userIDToMatrix(uid, ""))
	}
	roomID, err := r.rc.CreateGroup(ctx, params.Name.Name, mxids)
	if err != nil {
		return nil, fmt.Errorf("create group: %w", err)
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
