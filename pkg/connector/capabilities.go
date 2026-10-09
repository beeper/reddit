package connector

import (
	"context"
	"time"

	"go.mau.fi/util/jsontime"
	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
)

func (rc *RedditConnector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return &bridgev2.NetworkGeneralCapabilities{
		Provisioning: bridgev2.ProvisioningCapabilities{
			ResolveIdentifier: bridgev2.ResolveIdentifierCapabilities{
				CreateDM: true,
				Search:   true,
			},
			GroupCreation: map[string]bridgev2.GroupTypeCapabilities{
				"group": {
					TypeDescription: "a Reddit group chat",
					Name:            bridgev2.GroupFieldCapability{Allowed: true, Required: true, MaxLength: 300},
					Participants:    bridgev2.GroupFieldCapability{Allowed: true, Required: true, MinLength: 2},
				},
			},
		},
	}
}

const (
	MaxTextLength = 10_000
	MaxFileSize   = 20 * 1024 * 1024
)

var fileCaps = event.FileFeatureMap{
	event.CapMsgGIF: {
		MimeTypes: map[string]event.CapabilitySupportLevel{"image/gif": event.CapLevelFullySupported},
		Caption:   event.CapLevelRejected,
		MaxSize:   MaxFileSize,
	},
	event.MsgImage: {
		MimeTypes: map[string]event.CapabilitySupportLevel{
			"image/gif":  event.CapLevelFullySupported,
			"image/jpeg": event.CapLevelFullySupported,
			"image/png":  event.CapLevelFullySupported,
			"image/webp": event.CapLevelFullySupported,
		},
		// Reddit keeps the body as image alt text, but does not show captions.
		// Its own composer sends accompanying text as a separate message.
		Caption: event.CapLevelRejected,
		MaxSize: MaxFileSize,
	},
	event.MsgVideo: {
		MimeTypes: map[string]event.CapabilitySupportLevel{
			"*/*": event.CapLevelRejected,
		},
	},
	event.MsgFile: {
		MimeTypes: map[string]event.CapabilitySupportLevel{
			"*/*": event.CapLevelRejected,
		},
	},
}

var stateCaps = event.StateFeatureMap{
	event.StateRoomName.Type: {Level: event.CapLevelFullySupported},
}

func (*RedditClient) GetCapabilities(ctx context.Context, portal *bridgev2.Portal) *event.RoomFeatures {
	caps := (&event.RoomFeatures{
		ID:              "com.beeper.reddit.capabilities.v10",
		File:            fileCaps,
		MaxTextLength:   MaxTextLength,
		LocationMessage: event.CapLevelDropped,
		Reply:           event.CapLevelFullySupported,
		Thread:          event.CapLevelPartialSupport,
		Edit:            event.CapLevelRejected,
		Delete:          event.CapLevelFullySupported,
		DeleteChat:      true,
		DeleteMaxAge:    ptr.Ptr(jsontime.S(60 * time.Minute)),
		Reaction:        event.CapLevelFullySupported,
		MessageRequest: &event.MessageRequestFeatures{
			AcceptWithButton: event.CapLevelFullySupported,
			// The SDK accepts first, then sends the user's message.
			AcceptWithMessage: event.CapLevelPartialSupport,
		},
		ReadReceipts:        true,
		TypingNotifications: true,
		State:               stateCaps,
	}).Clone()
	if portal != nil && portal.RoomType == database.RoomTypeDM {
		caps.ID += ".dm"
		caps.State[event.StateRoomName.Type].Level = event.CapLevelRejected
	}
	return caps
}
