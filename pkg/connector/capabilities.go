package connector

import (
	"context"
	"time"

	"go.mau.fi/util/jsontime"
	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix/bridgev2"
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

var formattingCaps = event.FormattingFeatureMap{
	event.FmtBold:          event.CapLevelFullySupported,
	event.FmtItalic:        event.CapLevelFullySupported,
	event.FmtStrikethrough: event.CapLevelFullySupported,
	event.FmtInlineCode:    event.CapLevelFullySupported,
	event.FmtCodeBlock:     event.CapLevelFullySupported,
	event.FmtBlockquote:    event.CapLevelFullySupported,
	event.FmtInlineLink:    event.CapLevelFullySupported,
	event.FmtUserLink:      event.CapLevelFullySupported,
	event.FmtUnorderedList: event.CapLevelFullySupported,
	event.FmtOrderedList:   event.CapLevelFullySupported,
}

var fileCaps = event.FileFeatureMap{
	event.MsgImage: {
		MimeTypes: map[string]event.CapabilitySupportLevel{
			"image/gif":  event.CapLevelFullySupported,
			"image/jpeg": event.CapLevelFullySupported,
			"image/png":  event.CapLevelFullySupported,
			"image/webp": event.CapLevelFullySupported,
		},
		Caption:          event.CapLevelFullySupported,
		MaxCaptionLength: MaxTextLength,
		MaxSize:          MaxFileSize,
	},
	event.MsgVideo: {
		MimeTypes: map[string]event.CapabilitySupportLevel{
			"video/mp4":       event.CapLevelFullySupported,
			"video/quicktime": event.CapLevelFullySupported,
		},
		Caption:          event.CapLevelFullySupported,
		MaxCaptionLength: MaxTextLength,
		MaxSize:          MaxFileSize,
	},
	event.MsgFile: {
		MimeTypes: map[string]event.CapabilitySupportLevel{
			"*/*": event.CapLevelFullySupported,
		},
		Caption:          event.CapLevelFullySupported,
		MaxCaptionLength: MaxTextLength,
		MaxSize:          MaxFileSize,
	},
}

var stateCaps = event.StateFeatureMap{
	event.StateRoomName.Type: {Level: event.CapLevelFullySupported},
}

func (*RedditClient) GetCapabilities(ctx context.Context, portal *bridgev2.Portal) *event.RoomFeatures {
	return &event.RoomFeatures{
		ID:                  "com.beeper.reddit.capabilities.v1",
		Formatting:          formattingCaps,
		File:                fileCaps,
		MaxTextLength:       MaxTextLength,
		LocationMessage:     event.CapLevelDropped,
		Reply:               event.CapLevelFullySupported,
		Edit:                event.CapLevelFullySupported,
		EditMaxAge:          ptr.Ptr(jsontime.S(60 * time.Minute)),
		Delete:              event.CapLevelFullySupported,
		DeleteMaxAge:        ptr.Ptr(jsontime.S(60 * time.Minute)),
		Reaction:            event.CapLevelFullySupported,
		ReadReceipts:        true,
		TypingNotifications: true,
		State:               stateCaps,
		MemberActions: map[event.MemberAction]event.CapabilitySupportLevel{
			event.MemberActionInvite: event.CapLevelFullySupported,
			event.MemberActionKick:   event.CapLevelFullySupported,
		},
	}
}
