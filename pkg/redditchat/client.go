package redditchat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

const (
	DefaultHomeserver = "https://matrix.redditspace.com"
	DefaultChatURL    = "https://www.reddit.com/chat/"

	PresetRedditDM    = "reddit_dm"
	PresetPrivateChat = "private_chat"
)

type Credentials struct {
	Homeserver  string `json:"homeserver"`
	AccessToken string `json:"access_token"`
	UserID      string `json:"user_id"`
	DeviceID    string `json:"device_id,omitempty"`
}

type Client struct {
	Matrix *mautrix.Client
}

type UserDirectorySearchResponse struct {
	Results []UserDirectoryResult `json:"results"`
	Limited bool                  `json:"limited"`
}

type UserDirectoryResult struct {
	UserID      id.UserID           `json:"user_id"`
	DisplayName string              `json:"display_name"`
	AvatarURL   id.ContentURIString `json:"avatar_url"`
}

type RoomsWithUserResponse struct {
	Rooms []RoomSummary `json:"rooms"`
}

type RoomSummary struct {
	RoomID     id.RoomID      `json:"room_id"`
	Membership string         `json:"membership"`
	Chunk      []*event.Event `json:"chunk"`
}

type RoomCreateOptions struct {
	Visibility   string
	Name         string
	Topic        string
	Invite       []id.UserID
	Direct       bool
	Preset       string
	InitialState []*event.Event
}

type MediaInfo struct {
	MimeType string
	Size     int
	Width    int
	Height   int
	Duration int
}

type MediaConfigResponse struct {
	UploadSize       int64            `json:"m.upload.size,omitempty"`
	RedditUploadSize map[string]int64 `json:"com.reddit.upload.size,omitempty"`
}

type RedditError struct {
	StatusCode    int
	ErrCode       string
	Message       string
	RedditCode    string
	RedditMessage string
	Raw           map[string]any
}

func (e *RedditError) Error() string {
	parts := []string{fmt.Sprintf("matrix http %d", e.StatusCode)}
	if e.ErrCode != "" {
		parts = append(parts, e.ErrCode)
	}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	if e.RedditCode != "" {
		parts = append(parts, e.RedditCode)
	}
	if e.RedditMessage != "" && e.RedditMessage != e.Message {
		parts = append(parts, e.RedditMessage)
	}
	return strings.Join(parts, ": ")
}

func New(creds Credentials) (*Client, error) {
	if creds.AccessToken == "" {
		return nil, errors.New("reddit chat: missing Matrix access token")
	}
	if creds.UserID == "" {
		return nil, errors.New("reddit chat: missing Matrix user ID")
	}
	hs := strings.TrimRight(creds.Homeserver, "/")
	if hs == "" {
		hs = DefaultHomeserver
	}
	mx, err := mautrix.NewClient(hs, id.UserID(creds.UserID), creds.AccessToken)
	if err != nil {
		return nil, err
	}
	mx.DeviceID = id.DeviceID(creds.DeviceID)
	mx.UserAgent = "reddit-chat-go/0.1"
	return &Client{Matrix: mx}, nil
}

func (c *Client) Mautrix() *mautrix.Client {
	if c == nil {
		return nil
	}
	return c.Matrix
}

func (c *Client) SetHTTPClient(httpClient *http.Client) {
	c.Matrix.Client = httpClient
}

func (c *Client) Whoami(ctx context.Context) (*mautrix.RespWhoami, error) {
	return c.Matrix.Whoami(ctx)
}

func (c *Client) Capabilities(ctx context.Context) (*mautrix.RespCapabilities, error) {
	return c.Matrix.Capabilities(ctx)
}

func (c *Client) Sync(ctx context.Context, since string, timeoutMS int) (*mautrix.RespSync, error) {
	return c.Matrix.SyncRequest(ctx, timeoutMS, since, redditSyncFilter(), false, event.PresenceOffline)
}

func (c *Client) JoinedRooms(ctx context.Context) (*mautrix.RespJoinedRooms, error) {
	resp, err := c.Matrix.JoinedRooms(ctx)
	return resp, err
}

func (c *Client) Messages(ctx context.Context, roomID id.RoomID, from, to string, limit int) (*mautrix.RespMessages, error) {
	return c.Matrix.Messages(ctx, roomID, from, to, mautrix.DirectionBackward, nil, limit)
}

func (c *Client) GetEvent(ctx context.Context, roomID id.RoomID, eventID id.EventID) (*event.Event, error) {
	return c.Matrix.GetEvent(ctx, roomID, eventID)
}

func (c *Client) SearchUsers(ctx context.Context, term string) (*UserDirectorySearchResponse, error) {
	var resp UserDirectorySearchResponse
	_, err := c.Matrix.MakeRequest(ctx, http.MethodPost, c.Matrix.BuildClientURL("v3", "user_directory", "search"), map[string]string{
		"search_term": term,
	}, &resp)
	return &resp, err
}

func (c *Client) RoomsWithUser(ctx context.Context, matrixUserID id.UserID, roomType string, includes ...string) (*RoomsWithUserResponse, error) {
	query := map[string]string{
		"with_user": matrixUserID.String(),
	}
	if roomType != "" {
		query["type"] = roomType
	}
	if len(includes) > 0 {
		query["include"] = strings.Join(includes, ",")
	}
	var resp RoomsWithUserResponse
	_, err := c.Matrix.MakeRequest(ctx, http.MethodGet, c.Matrix.BuildURLWithQuery(mautrix.ClientURLPath{"v3", "rooms"}, query), nil, &resp)
	return &resp, err
}

func (c *Client) DirectRoomsWithUser(ctx context.Context, matrixUserID id.UserID) (*RoomsWithUserResponse, error) {
	return c.RoomsWithUser(ctx, matrixUserID, "direct", "state", "timeline")
}

func (c *Client) CreateRoom(ctx context.Context, opts RoomCreateOptions) (*mautrix.RespCreateRoom, error) {
	preset := opts.Preset
	if preset == "" {
		preset = PresetPrivateChat
	}
	visibility := opts.Visibility
	if visibility == "" && preset != PresetRedditDM {
		visibility = "private"
	}
	initial := opts.InitialState
	return c.Matrix.CreateRoom(ctx, &mautrix.ReqCreateRoom{
		Visibility:   visibility,
		Preset:       preset,
		IsDirect:     opts.Direct,
		Name:         opts.Name,
		Topic:        opts.Topic,
		Invite:       opts.Invite,
		InitialState: initial,
	})
}

func (c *Client) CreateDM(ctx context.Context, matrixUserID id.UserID) (id.RoomID, error) {
	resp, err := c.CreateRoom(ctx, RoomCreateOptions{
		Invite: []id.UserID{matrixUserID},
		Preset: PresetRedditDM,
	})
	if err != nil {
		return "", err
	}
	return resp.RoomID, nil
}

func (c *Client) CreateOrGetDM(ctx context.Context, matrixUserID id.UserID) (roomID id.RoomID, created bool, err error) {
	rooms, err := c.DirectRoomsWithUser(ctx, matrixUserID)
	if err != nil {
		return "", false, err
	}
	for _, room := range rooms.Rooms {
		if room.RoomID != "" && (room.Membership == "join" || room.Membership == "invite") {
			return room.RoomID, false, nil
		}
	}
	roomID, err = c.CreateDM(ctx, matrixUserID)
	if err != nil {
		if existing := existingRoomIDFromError(err); existing != "" {
			return existing, false, nil
		}
	}
	return roomID, true, err
}

func (c *Client) CreateGroup(ctx context.Context, name string, matrixUserIDs []id.UserID) (id.RoomID, error) {
	initialInvite := matrixUserIDs
	restInvite := []id.UserID(nil)
	if len(matrixUserIDs) > 10 {
		initialInvite = matrixUserIDs[:10]
		restInvite = matrixUserIDs[10:]
	}
	resp, err := c.CreateRoom(ctx, RoomCreateOptions{
		Visibility: "private",
		Name:       name,
		Invite:     initialInvite,
		Direct:     false,
		Preset:     PresetPrivateChat,
	})
	if err != nil {
		return "", err
	}
	for _, matrixUserID := range restInvite {
		if err := c.Invite(ctx, resp.RoomID, matrixUserID); err != nil {
			return "", err
		}
	}
	return resp.RoomID, nil
}

func (c *Client) Invite(ctx context.Context, roomID id.RoomID, matrixUserID id.UserID) error {
	_, err := c.Matrix.InviteUser(ctx, roomID, &mautrix.ReqInviteUser{UserID: matrixUserID})
	return err
}

func (c *Client) AcceptInvite(ctx context.Context, roomID id.RoomID) error {
	_, err := c.Matrix.JoinRoomByID(ctx, roomID)
	return err
}

func (c *Client) Leave(ctx context.Context, roomID id.RoomID) error {
	_, err := c.Matrix.LeaveRoom(ctx, roomID)
	return err
}

func (c *Client) SetTyping(ctx context.Context, roomID id.RoomID, typing bool, timeout time.Duration) error {
	_, err := c.Matrix.UserTyping(ctx, roomID, typing, timeout)
	return err
}

func (c *Client) MarkRead(ctx context.Context, roomID id.RoomID, eventID id.EventID) error {
	return c.Matrix.MarkRead(ctx, roomID, eventID)
}

func (c *Client) SendText(ctx context.Context, roomID id.RoomID, body string) (*mautrix.RespSendEvent, error) {
	return c.Matrix.SendText(ctx, roomID, body)
}

func (c *Client) SendNotice(ctx context.Context, roomID id.RoomID, body string) (*mautrix.RespSendEvent, error) {
	return c.Matrix.SendNotice(ctx, roomID, body)
}

func (c *Client) SendMessage(ctx context.Context, roomID id.RoomID, content *event.MessageEventContent) (*mautrix.RespSendEvent, error) {
	return c.Matrix.SendMessageEvent(ctx, roomID, event.EventMessage, content)
}

func (c *Client) UploadMedia(ctx context.Context, filename, contentType string, r io.Reader) (*mautrix.RespMediaUpload, int, error) {
	contentType = mediaContentType(filename, contentType)
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		return nil, 0, err
	}
	resp, err := c.Matrix.UploadMedia(ctx, mautrix.ReqUploadMedia{
		ContentBytes: buf.Bytes(),
		ContentType:  contentType,
		FileName:     filename,
	})
	if err != nil {
		return nil, buf.Len(), err
	}
	return resp, buf.Len(), nil
}

func (c *Client) MediaConfig(ctx context.Context) (*MediaConfigResponse, error) {
	var resp MediaConfigResponse
	_, err := c.Matrix.MakeRequest(ctx, http.MethodGet, c.Matrix.BuildURL(mautrix.MediaURLPath{"v3", "config"}), nil, &resp)
	return &resp, err
}

func (c *Client) DownloadMedia(ctx context.Context, uri id.ContentURI) (*http.Response, error) {
	return c.Matrix.Download(ctx, uri)
}

func (c *Client) DownloadMediaBytes(ctx context.Context, uri id.ContentURI) ([]byte, error) {
	return c.Matrix.DownloadBytes(ctx, uri)
}

func (c *Client) SendFile(ctx context.Context, roomID id.RoomID, filename, contentType string, r io.Reader) (*mautrix.RespSendEvent, error) {
	return c.SendMedia(ctx, roomID, event.MsgFile, filename, contentType, MediaInfo{}, r)
}

func (c *Client) SendImage(ctx context.Context, roomID id.RoomID, filename, contentType string, width, height int, r io.Reader) (*mautrix.RespSendEvent, error) {
	return c.SendMedia(ctx, roomID, event.MsgImage, filename, contentType, MediaInfo{Width: width, Height: height}, r)
}

func (c *Client) SendVideo(ctx context.Context, roomID id.RoomID, filename, contentType string, width, height, durationMS int, r io.Reader) (*mautrix.RespSendEvent, error) {
	return c.SendMedia(ctx, roomID, event.MsgVideo, filename, contentType, MediaInfo{Width: width, Height: height, Duration: durationMS}, r)
}

func (c *Client) SendAudio(ctx context.Context, roomID id.RoomID, filename, contentType string, durationMS int, r io.Reader) (*mautrix.RespSendEvent, error) {
	return c.SendMedia(ctx, roomID, event.MsgAudio, filename, contentType, MediaInfo{Duration: durationMS}, r)
}

func (c *Client) SendHostedFile(ctx context.Context, roomID id.RoomID, filename, contentType, uri string, size int) (*mautrix.RespSendEvent, error) {
	return c.SendMXCFile(ctx, roomID, filename, contentType, uri, size)
}

func (c *Client) SendHostedImage(ctx context.Context, roomID id.RoomID, filename, contentType, uri string, width, height, size int) (*mautrix.RespSendEvent, error) {
	return c.SendMXCImage(ctx, roomID, filename, contentType, uri, width, height, size)
}

func (c *Client) SendHostedVideo(ctx context.Context, roomID id.RoomID, filename, contentType, uri string, width, height, durationMS, size int) (*mautrix.RespSendEvent, error) {
	return c.SendMXCVideo(ctx, roomID, filename, contentType, uri, width, height, durationMS, size)
}

func (c *Client) SendHostedAudio(ctx context.Context, roomID id.RoomID, filename, contentType, uri string, durationMS, size int) (*mautrix.RespSendEvent, error) {
	return c.SendMXCAudio(ctx, roomID, filename, contentType, uri, durationMS, size)
}

func (c *Client) SendHostedMedia(ctx context.Context, roomID id.RoomID, msgType event.MessageType, filename, uri string, info MediaInfo) (*mautrix.RespSendEvent, error) {
	return c.SendMXCMedia(ctx, roomID, msgType, filename, uri, info)
}

func (c *Client) SendMXCFile(ctx context.Context, roomID id.RoomID, filename, contentType, uri string, size int) (*mautrix.RespSendEvent, error) {
	return c.SendMXCMedia(ctx, roomID, event.MsgFile, filename, uri, MediaInfo{MimeType: contentType, Size: size})
}

func (c *Client) SendMXCImage(ctx context.Context, roomID id.RoomID, filename, contentType, uri string, width, height, size int) (*mautrix.RespSendEvent, error) {
	return c.SendMXCMedia(ctx, roomID, event.MsgImage, filename, uri, MediaInfo{MimeType: contentType, Width: width, Height: height, Size: size})
}

func (c *Client) SendMXCVideo(ctx context.Context, roomID id.RoomID, filename, contentType, uri string, width, height, durationMS, size int) (*mautrix.RespSendEvent, error) {
	return c.SendMXCMedia(ctx, roomID, event.MsgVideo, filename, uri, MediaInfo{MimeType: contentType, Width: width, Height: height, Duration: durationMS, Size: size})
}

func (c *Client) SendMXCAudio(ctx context.Context, roomID id.RoomID, filename, contentType, uri string, durationMS, size int) (*mautrix.RespSendEvent, error) {
	return c.SendMXCMedia(ctx, roomID, event.MsgAudio, filename, uri, MediaInfo{MimeType: contentType, Duration: durationMS, Size: size})
}

func (c *Client) SendMXCMedia(ctx context.Context, roomID id.RoomID, msgType event.MessageType, filename, uri string, info MediaInfo) (*mautrix.RespSendEvent, error) {
	contentType := mediaContentType(filename, info.MimeType)
	content := &event.MessageEventContent{
		MsgType: msgType,
		Body:    filename,
		URL:     id.ContentURIString(uri),
		Info: &event.FileInfo{
			MimeType: contentType,
			Size:     info.Size,
			Width:    info.Width,
			Height:   info.Height,
			Duration: info.Duration,
		},
	}
	if msgType == event.MsgFile {
		content.FileName = filename
	}
	return c.SendMessage(ctx, roomID, content)
}

func (c *Client) SendMedia(ctx context.Context, roomID id.RoomID, msgType event.MessageType, filename, contentType string, info MediaInfo, r io.Reader) (*mautrix.RespSendEvent, error) {
	contentType = mediaContentType(filename, firstNonEmpty(contentType, info.MimeType))
	up, size, err := c.UploadMedia(ctx, filename, contentType, r)
	if err != nil {
		return nil, err
	}
	fileInfo := &event.FileInfo{
		MimeType: contentType,
		Size:     size,
		Width:    info.Width,
		Height:   info.Height,
		Duration: info.Duration,
	}
	content := &event.MessageEventContent{
		MsgType: msgType,
		Body:    filename,
		URL:     up.ContentURI.CUString(),
		Info:    fileInfo,
	}
	if msgType == event.MsgFile {
		content.FileName = filename
	}
	return c.SendMessage(ctx, roomID, content)
}

func mediaContentType(filename, contentType string) string {
	if contentType != "" {
		return contentType
	}
	if guessed := mime.TypeByExtension(path.Ext(filename)); guessed != "" {
		return guessed
	}
	return "application/octet-stream"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func ExtractRedditError(err error) (*RedditError, bool) {
	var httpErr mautrix.HTTPError
	if !errors.As(err, &httpErr) || httpErr.RespError == nil {
		return nil, false
	}
	out := &RedditError{
		ErrCode: httpErr.RespError.ErrCode,
		Message: httpErr.RespError.Err,
		Raw:     httpErr.RespError.ExtraData,
	}
	if httpErr.Response != nil {
		out.StatusCode = httpErr.Response.StatusCode
	}
	if v, ok := httpErr.RespError.ExtraData["com.reddit.error.code"].(string); ok {
		out.RedditCode = v
	}
	if v, ok := httpErr.RespError.ExtraData["com.reddit.error.msg"].(string); ok {
		out.RedditMessage = v
	}
	return out, true
}

func existingRoomIDFromError(err error) id.RoomID {
	redditErr, ok := ExtractRedditError(err)
	if !ok {
		return ""
	}
	if v, ok := redditErr.Raw["com.reddit.existing_room_id"].(string); ok {
		return id.RoomID(v)
	}
	return ""
}

func redditSyncFilter() string {
	return `{"room":{"timeline":{"unread_thread_notifications":true,"not_types":["com.reddit.review_open","com.reddit.review_close"],"lazy_load_members":true},"state":{"lazy_load_members":true}}}`
}
