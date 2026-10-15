package connector

import (
	"context"
	"errors"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
)

var _ bridgev2.UserBlockingNetworkAPI = (*RedditClient)(nil)

func (r *RedditClient) HandleMatrixBlockUser(ctx context.Context, msg *bridgev2.MatrixBlockUser) error {
	if msg.Content.ReportSpam {
		return errors.New("spam reporting is not supported")
	}
	if r.session == nil {
		return errors.New("blocking requires a saved Reddit session; sign in again")
	}
	userID := msg.Portal.OtherUserID
	if userID == "" || userID == r.userID {
		return errors.New("could not identify the other Reddit user in this DM")
	}
	// Serialize native snapshots with mutations so an older response cannot
	// overwrite the new state in the account's in-memory block list.
	r.blockMu.Lock()
	defer r.blockMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := r.session.SetUserBlocked(ctx, string(userID), msg.Content.Block); err != nil {
		return err
	}
	if r.blockedUsers != nil {
		r.blockedUsers[userID] = msg.Content.Block
	}
	// bridgev2 updates and persists this portal only after this call succeeds.
	return nil
}

func (r *RedditClient) userBlocked(userID networkid.UserID) *bool {
	r.blockMu.Lock()
	defer r.blockMu.Unlock()
	if r.blockedUsers == nil || userID == "" {
		return nil
	}
	blocked := r.blockedUsers[userID]
	return &blocked
}

func (r *RedditClient) refreshBlockedUsers(ctx context.Context) ([]string, error) {
	r.blockMu.Lock()
	defer r.blockMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	users, err := r.session.GetBlockedUsers(ctx)
	if err != nil {
		return nil, err
	}
	r.blockedUsers = make(map[networkid.UserID]bool, len(users))
	for _, user := range users {
		r.blockedUsers[networkid.UserID(user)] = true
	}
	return users, nil
}

// Reddit's block list is outside Matrix /sync. Poll it on the existing sync
// loop and reconcile persisted DM flags, including unblocks missed offline.
func (r *RedditClient) syncBlockedUsers(ctx context.Context) error {
	users, err := r.refreshBlockedUsers(ctx)
	if err != nil {
		return err
	}
	portals := r.main.Bridge.DB.Portal
	keys, err := portals.GetAllBlockedDMsOf(ctx, r.userLogin.ID)
	if err != nil {
		return err
	}
	targets := make(map[networkid.PortalKey]networkid.UserID)
	for _, key := range keys {
		portal, err := portals.GetByKey(ctx, key)
		if err != nil {
			return err
		}
		if portal != nil {
			targets[key] = portal.OtherUserID
		}
	}
	for _, user := range users {
		dms, err := portals.GetAllDMsWith(ctx, networkid.UserID(user))
		if err != nil {
			return err
		}
		for _, dm := range dms {
			if dm.Receiver == r.userLogin.ID {
				targets[dm.PortalKey] = dm.OtherUserID
			}
		}
	}
	for key, userID := range targets {
		r.userLogin.QueueRemoteEvent(&blockListChange{
			EventMeta: simplevent.EventMeta{Type: bridgev2.RemoteEventChatInfoChange, PortalKey: key},
			client:    r,
			userID:    userID,
		})
	}
	return nil
}

type blockListChange struct {
	simplevent.EventMeta
	client *RedditClient
	userID networkid.UserID
}

var _ bridgev2.RemoteChatInfoChange = (*blockListChange)(nil)

func (evt *blockListChange) GetChatInfoChange(context.Context) (*bridgev2.ChatInfoChange, error) {
	// Read at handling time: a Beeper block/unblock may have completed while
	// this native snapshot was waiting in the portal's event queue.
	return &bridgev2.ChatInfoChange{ChatInfo: &bridgev2.ChatInfo{
		UserBlocked: evt.client.userBlocked(evt.userID),
	}}, nil
}
