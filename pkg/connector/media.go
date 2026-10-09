package connector

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func imageMIME(data []byte) (string, error) {
	mime := http.DetectContentType(data)
	switch mime {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return mime, nil
	default:
		return "", bridgev2.ErrUnsupportedMediaType
	}
}

func readMediaBounded(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, errors.New("failed to read attachment")
	}
	if int64(len(data)) > limit {
		return nil, bridgev2.ErrMediaTooLarge
	}
	return data, nil
}

// Do not expose network error strings: redirected media URLs may carry bearer
// capabilities. Keep cancellation and a bounded HTTP status for diagnosis.
func mediaError(ctx context.Context, operation string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	safe := fmt.Errorf("%s failed", operation)
	var httpErr mautrix.HTTPError
	if errors.As(err, &httpErr) && httpErr.Response != nil {
		safe = fmt.Errorf("%s failed (HTTP %d)", operation, httpErr.Response.StatusCode)
	}
	if isPermanentHTTPError(err) {
		return unbridgeable(safe)
	}
	return safe
}

func copyMediaInfo(content *event.MessageEventContent) {
	if content.Info == nil {
		content.Info = &event.FileInfo{}
	} else {
		copied := *content.Info
		content.Info = &copied
	}
}

func (r *RedditClient) incomingMediaLimit() int64 {
	if limit := r.main.mediaUploadLimit.Load(); limit > 0 {
		return limit
	}
	return MaxFileSize
}

func (r *RedditClient) downloadRedditMedia(ctx context.Context, uri id.ContentURIString, file *event.EncryptedFileInfo) ([]byte, error) {
	if file != nil {
		uri = file.URL
	}
	parsed, err := uri.Parse()
	if err != nil || parsed.IsEmpty() || parsed.Homeserver != "reddit.com" {
		return nil, unbridgeable(errors.New("attachment is not a Reddit media URI"))
	}
	resp, err := r.remote().DownloadMedia(ctx, parsed)
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		return nil, mediaError(ctx, "Reddit media download", err)
	}
	defer resp.Body.Close()
	if resp.ContentLength > r.incomingMediaLimit() {
		return nil, bridgev2.ErrMediaTooLarge
	}
	data, err := readMediaBounded(resp.Body, r.incomingMediaLimit())
	if err != nil {
		return nil, err
	}
	if file != nil {
		if err = file.DecryptInPlace(data); err != nil {
			return nil, unbridgeable(errors.New("reddit attachment failed decryption or integrity verification"))
		}
	}
	return data, nil
}

func (r *RedditClient) receiveMedia(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, content *event.MessageEventContent) error {
	// Encrypted native files keep the verified reupload path: their keys must
	// never be embedded in public media IDs. Fall back there for oversized IDs
	// too, since the framework limits the length of signed content URIs.
	if r.main.directMedia && content.File == nil && (content.Info == nil || content.Info.ThumbnailFile == nil) {
		url, err := r.directMediaURI(ctx, content.URL)
		var thumbnail id.ContentURIString
		if err == nil && content.Info != nil && content.Info.ThumbnailURL != "" {
			thumbnail, err = r.directMediaURI(ctx, content.Info.ThumbnailURL)
		}
		if err == nil {
			content.URL = url
			copyMediaInfo(content)
			content.Info.ThumbnailURL = thumbnail
			return nil
		}
	}
	if intent == nil {
		return errors.New("media conversion requires a Matrix sender")
	}
	data, err := r.downloadRedditMedia(ctx, content.URL, content.File)
	if err != nil {
		return err
	}
	mime, err := imageMIME(data)
	if err != nil {
		return err
	}
	url, file, err := intent.UploadMedia(ctx, portal.MXID, data, content.GetFileName(), mime)
	if err != nil {
		return mediaError(ctx, "Beeper media upload", err)
	}
	content.URL, content.File = url, file
	copyMediaInfo(content)
	content.Info.Size, content.Info.MimeType = len(data), mime
	if content.Info.ThumbnailURL != "" || content.Info.ThumbnailFile != nil {
		thumb, err := r.downloadRedditMedia(ctx, content.Info.ThumbnailURL, content.Info.ThumbnailFile)
		if err != nil {
			return err
		}
		thumbMIME, err := imageMIME(thumb)
		if err != nil {
			return err
		}
		content.Info.ThumbnailURL, content.Info.ThumbnailFile, err = intent.UploadMedia(ctx, portal.MXID, thumb, "thumbnail", thumbMIME)
		if err != nil {
			return mediaError(ctx, "Beeper thumbnail upload", err)
		}
	}
	return nil
}

func (r *RedditClient) uploadRedditMedia(ctx context.Context, intent bridgev2.MatrixAPI, uri id.ContentURIString, file *event.EncryptedFileInfo, filename string) (id.ContentURIString, int, string, error) {
	config, err := r.remote().MediaConfig(ctx)
	if err != nil {
		return "", 0, "", mediaError(ctx, "Reddit media limits", err)
	}
	limit := config.UploadSize
	// Fetch at most the provider's largest supported image allowance before
	// sniffing MIME. Apply the exact MIME-specific limit before uploading.
	for mime, size := range config.RedditUploadSize {
		if _, supported := fileCaps[event.MsgImage].MimeTypes[mime]; supported && size > limit {
			limit = size
		}
	}
	if limit <= 0 {
		return "", 0, "", errors.New("reddit did not provide a valid upload limit")
	}
	var data []byte
	err = intent.DownloadMediaToFile(ctx, uri, file, false, func(f *os.File) error {
		var readErr error
		data, readErr = readMediaBounded(f, limit)
		return readErr
	})
	if err != nil {
		if errors.Is(err, bridgev2.ErrMediaTooLarge) {
			return "", 0, "", err
		}
		return "", 0, "", mediaError(ctx, "Beeper media download", err)
	}
	mime, err := imageMIME(data)
	if err != nil {
		return "", 0, "", err
	}
	limit = config.UploadSize
	if override, ok := config.RedditUploadSize[mime]; ok {
		limit = override
	}
	if int64(len(data)) > limit || limit <= 0 {
		return "", 0, "", bridgev2.ErrMediaTooLarge
	}
	upload, _, err := r.remote().UploadMedia(ctx, filename, mime, bytes.NewReader(data))
	if err != nil {
		return "", 0, "", mediaError(ctx, "Reddit media upload", err)
	}
	if upload.ContentURI.IsEmpty() || upload.ContentURI.Homeserver != "reddit.com" {
		return "", 0, "", errors.New("reddit upload returned an invalid media identity")
	}
	return upload.ContentURI.CUString(), len(data), mime, nil
}

func (r *RedditClient) sendMedia(ctx context.Context, content *event.MessageEventContent) error {
	url, size, mime, err := r.uploadRedditMedia(ctx, r.main.Bridge.Bot, content.URL, content.File, content.GetFileName())
	if err != nil {
		return err
	}
	content.URL, content.File = url, nil
	copyMediaInfo(content)
	content.Info.Size, content.Info.MimeType = size, mime
	if content.Info.ThumbnailURL != "" || content.Info.ThumbnailFile != nil {
		content.Info.ThumbnailURL, _, _, err = r.uploadRedditMedia(ctx, r.main.Bridge.Bot, content.Info.ThumbnailURL, content.Info.ThumbnailFile, "thumbnail")
		if err != nil {
			return err
		}
		content.Info.ThumbnailFile = nil
	}
	return nil
}
