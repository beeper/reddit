package connector

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type RedditConnector struct {
	Bridge *bridgev2.Bridge
	Config Config
}

var (
	_ bridgev2.NetworkConnector               = (*RedditConnector)(nil)
	_ bridgev2.ConfigValidatingNetwork        = (*RedditConnector)(nil)
	_ bridgev2.TransactionIDGeneratingNetwork = (*RedditConnector)(nil)
)

func (rc *RedditConnector) Init(bridge *bridgev2.Bridge) {
	rc.Bridge = bridge
}

func (rc *RedditConnector) Start(ctx context.Context) error {
	return nil
}

func (rc *RedditConnector) GetName() bridgev2.BridgeName {
	return bridgev2.BridgeName{
		DisplayName:      "Reddit",
		NetworkURL:       "https://reddit.com",
		NetworkIcon:      "",
		NetworkID:        "reddit",
		BeeperBridgeType: "reddit",
		DefaultPort:      29346,
	}
}

func (rc *RedditConnector) GetBridgeInfoVersion() (info, capabilities int) {
	return 1, 1
}

func (rc *RedditConnector) GenerateTransactionID(userID id.UserID, roomID id.RoomID, eventType event.Type) networkid.RawTransactionID {
	return networkid.RawTransactionID(uuid.NewString())
}

func (rc *RedditConnector) LoadUserLogin(ctx context.Context, login *bridgev2.UserLogin) error {
	client, err := NewRedditClient(rc, login)
	if err != nil {
		return fmt.Errorf("create reddit client: %w", err)
	}
	login.Client = client
	return nil
}
