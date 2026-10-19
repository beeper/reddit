package connector

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"maunium.net/go/mautrix/bridgev2"

	"github.com/beeper/reddit/pkg/redditchat"
)

var _ bridgev2.PushableNetworkAPI = (*RedditClient)(nil)

var pushConfig = &bridgev2.PushConfig{
	FCM: &bridgev2.FCMPushConfig{SenderID: redditchat.FCMSenderID},
}

func (r *RedditClient) GetPushConfigs() *bridgev2.PushConfig {
	return pushConfig
}

func (r *RedditClient) RegisterPushNotifications(ctx context.Context, pushType bridgev2.PushType, token string) error {
	if pushType != bridgev2.PushTypeFCM {
		return fmt.Errorf("unsupported push type: %s", pushType)
	}
	if !r.IsLoggedIn() {
		return bridgev2.ErrNotLoggedIn
	}
	if strings.TrimSpace(token) == "" {
		return errors.New("reddit push: FCM token is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.pushMu.Lock()
	defer r.pushMu.Unlock()
	deviceID, err := r.pushDeviceID(ctx)
	if err != nil {
		return err
	}
	err = r.remote().RegisterFCMPush(ctx, token, deviceID)
	if errors.Is(err, redditchat.ErrPushAuthentication) && r.session != nil {
		if refreshErr := r.refreshChatToken(ctx); refreshErr != nil {
			return fmt.Errorf("refresh Reddit credentials for push registration: %w", refreshErr)
		}
		err = r.remote().RegisterFCMPush(ctx, token, deviceID)
	}
	return err
}

func (r *RedditClient) pushDeviceID(ctx context.Context) (string, error) {
	r.saveMu.Lock()
	defer r.saveMu.Unlock()
	if r.meta.PushDeviceID == "" {
		r.meta.PushDeviceID = uuid.NewString()
		if err := r.userLogin.Save(ctx); err != nil {
			r.meta.PushDeviceID = ""
			return "", fmt.Errorf("save Reddit push device ID: %w", err)
		}
	}
	return r.meta.PushDeviceID, nil
}
