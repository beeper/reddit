package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/gif"
	"image/png"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func TestBeeperGIFUsesNativeImageWithoutChangingSource(t *testing.T) {
	var encoded bytes.Buffer
	if err := gif.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil); err != nil {
		t.Fatal(err)
	}
	intent := &mediaTestIntent{download: encoded.Bytes()}
	sent := false
	r := testClient(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/_matrix/media/v3/config":
			_, _ = io.WriteString(w, `{"m.upload.size":20971520}`)
		case "/_matrix/media/v3/upload":
			data, _ := io.ReadAll(req.Body)
			if !bytes.Equal(data, encoded.Bytes()) || req.Header.Get("Content-Type") != "image/gif" {
				t.Error("GIF bytes changed")
			}
			_, _ = io.WriteString(w, `{"content_uri":"mxc://reddit.com/gif"}`)
		default:
			if !strings.Contains(req.URL.Path, "/send/m.room.message/") {
				t.Error("unexpected GIF request", req.URL.Path)
			}
			var content event.MessageEventContent
			if err := json.NewDecoder(req.Body).Decode(&content); err != nil {
				t.Error(err)
			}
			if content.MsgType != event.MsgImage || content.Info.MimeType != "image/gif" || content.Info.MauGIF || content.URL != "mxc://reddit.com/gif" {
				t.Error("wrong native GIF shape", content.MsgType, content.Info)
			}
			sent = true
			_, _ = io.WriteString(w, `{"event_id":"$gif"}`)
		}
	})
	r.main.Bridge.Bot = intent
	content := &event.MessageEventContent{MsgType: event.MsgVideo, Body: "test.gif", URL: "mxc://beeper.example/gif", Info: &event.FileInfo{MimeType: "image/gif", MauGIF: true}}
	response, err := r.HandleMatrixMessage(context.Background(), &bridgev2.MatrixMessage{MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{Portal: historyPortal(), Event: &event.Event{ID: "$source"}, Content: content}})
	if err != nil || !sent || response.DB.ID != "$gif" {
		t.Fatal("GIF send failed", err)
	}
	if content.MsgType != event.MsgVideo || !content.Info.MauGIF {
		t.Fatal("SDK-owned input changed")
	}
	caps := r.GetCapabilities(context.Background(), nil)
	if caps.File[event.CapMsgGIF].GetMimeSupport("image/gif") != event.CapLevelFullySupported || caps.File[event.CapMsgGIF].GetMimeSupport("video/mp4") != event.CapLevelRejected {
		t.Fatal("GIF capability differs from native support")
	}
}

type mediaTestIntent struct {
	bridgev2.MatrixAPI
	uploaded          [][]byte
	downloadedURI     id.ContentURIString
	encryptedDownload bool
	download          []byte
}

func (m *mediaTestIntent) UploadMedia(ctx context.Context, roomID id.RoomID, data []byte, filename, mime string) (id.ContentURIString, *event.EncryptedFileInfo, error) {
	m.uploaded = append(m.uploaded, append([]byte(nil), data...))
	return "", &event.EncryptedFileInfo{URL: "mxc://beeper.example/encrypted", EncryptedFile: *attachment.NewEncryptedFile()}, nil
}

func (m *mediaTestIntent) DownloadMediaToFile(ctx context.Context, uri id.ContentURIString, file *event.EncryptedFileInfo, writable bool, callback func(*os.File) error) error {
	m.downloadedURI = uri
	data := append([]byte(nil), m.download...)
	if file != nil {
		m.encryptedDownload = true
		m.downloadedURI = file.URL
		if err := file.DecryptInPlace(data); err != nil {
			return err
		}
	}
	f, err := os.CreateTemp("", "reddit-media-test-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return callback(f)
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func serializeMediaKey(t *testing.T, key *attachment.EncryptedFile) attachment.EncryptedFile {
	t.Helper()
	data, err := json.Marshal(key)
	if err != nil {
		t.Fatal(err)
	}
	var restored attachment.EncryptedFile
	if err = json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	return restored
}

func TestIncomingMediaReuploadsBodyAndThumbnailForEncryptedRoom(t *testing.T) {
	plain := testPNG(t)
	key := attachment.NewEncryptedFile()
	encrypted := bytes.Clone(plain)
	key.EncryptInPlace(encrypted)
	requests := 0
	r := testClient(t, func(w http.ResponseWriter, req *http.Request) {
		requests++
		if !strings.HasPrefix(req.URL.Path, "/_matrix/media/v3/download/reddit.com/") || req.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("download did not use the authenticated provider client")
		}
		if strings.HasSuffix(req.URL.Path, "/image") {
			_, _ = w.Write(encrypted)
		} else {
			_, _ = w.Write(plain)
		}
	})
	intent := &mediaTestIntent{}
	r.main.SetUseDirectMedia() // Native encryption must still use verified reupload.
	source := &event.MessageEventContent{MsgType: event.MsgImage, Body: "caption", FileName: "test.png", File: &event.EncryptedFileInfo{URL: "mxc://reddit.com/image", EncryptedFile: serializeMediaKey(t, key)}, Info: &event.FileInfo{ThumbnailURL: "mxc://reddit.com/thumb", MimeType: "image/wrong"}}
	converted, err := r.convertMessage(context.Background(), historyPortal(), intent, &event.Event{Content: event.Content{Parsed: source}})
	if err != nil {
		t.Fatal(err)
	}
	content := converted.Parts[0].Content
	if requests != 2 || len(intent.uploaded) != 2 || !bytes.Equal(intent.uploaded[0], plain) || !bytes.Equal(intent.uploaded[1], plain) {
		t.Fatal("attachment bytes were lost or not decrypted")
	}
	if content.URL != "" || content.File.URL != "mxc://beeper.example/encrypted" || content.Info.ThumbnailURL != "" || content.Info.ThumbnailFile.URL != "mxc://beeper.example/encrypted" || content.Body != "caption" || content.Info.MimeType != "image/png" {
		t.Fatal("media references or caption were not correctly replaced")
	}
	if source.File.URL != "mxc://reddit.com/image" || source.Info.ThumbnailURL != "mxc://reddit.com/thumb" {
		t.Fatal("source media mutated")
	}
}

func TestOutgoingEncryptedImageTransfersBytesBeforeSend(t *testing.T) {
	plain := testPNG(t)
	key := attachment.NewEncryptedFile()
	intent := &mediaTestIntent{download: encryptedCopy(key, plain)}
	uploaded := false
	r := testClient(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/_matrix/media/v3/config":
			_, _ = io.WriteString(w, `{"m.upload.size":20971520,"com.reddit.upload.size":{"image/gif":104857600}}`)
		case "/_matrix/media/v3/upload":
			data, _ := io.ReadAll(req.Body)
			if !bytes.Equal(data, plain) || req.Header.Get("Content-Type") != "image/png" {
				t.Error("uploaded incorrect bytes or MIME")
			}
			uploaded = true
			_, _ = io.WriteString(w, `{"content_uri":"mxc://reddit.com/uploaded"}`)
		default:
			t.Errorf("unexpected media request: %s", req.URL.Path)
		}
	})
	r.main.Bridge.Bot = intent
	content := &event.MessageEventContent{MsgType: event.MsgImage, Body: "caption", FileName: "photo.png", File: &event.EncryptedFileInfo{URL: "mxc://beeper.example/source", EncryptedFile: serializeMediaKey(t, key)}}
	if err := r.sendMedia(context.Background(), content); err != nil {
		t.Fatal(err)
	}
	if !uploaded || !intent.encryptedDownload || content.File != nil || content.URL != "mxc://reddit.com/uploaded" || content.Info.Size != len(plain) || content.Body != "caption" {
		t.Fatal("outgoing media was not transferred")
	}
}

func TestMediaRejectsOversizedForeignAndNonImageAttachments(t *testing.T) {
	r := testClient(t, func(w http.ResponseWriter, req *http.Request) {
		w.(http.Flusher).Flush() // Unknown length: enforce the bound while reading.
		_, _ = w.Write([]byte("too large"))
	})
	r.main.SetMaxFileSize(3)
	if _, err := r.downloadRedditMedia(context.Background(), "mxc://reddit.com/image", nil); !errors.Is(err, bridgev2.ErrMediaTooLarge) {
		t.Fatalf("oversized download: %v", err)
	}
	if _, err := r.downloadRedditMedia(context.Background(), "mxc://foreign.example/image", nil); err == nil {
		t.Fatal("foreign media accepted")
	}
	if _, err := imageMIME([]byte("<html>not an image</html>")); !errors.Is(err, bridgev2.ErrUnsupportedMediaType) {
		t.Fatal("non-image accepted")
	}
}

func TestProviderUploadRejectionCannotSendBrokenImageEvent(t *testing.T) {
	intent := &mediaTestIntent{download: testPNG(t)}
	r := testClient(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/_matrix/media/v3/config":
			_, _ = io.WriteString(w, `{"m.upload.size":20971520}`)
		case "/_matrix/media/v3/upload":
			w.WriteHeader(403)
			_, _ = io.WriteString(w, `{"errcode":"M_FORBIDDEN","error":"Media upload forbidden"}`)
		default:
			t.Error("message was sent after upload failed")
		}
	})
	r.main.Bridge.Bot = intent
	msg := outgoingTestMessage()
	msg.Content = &event.MessageEventContent{MsgType: event.MsgImage, Body: "image.png", URL: "mxc://beeper.example/source"}
	if resp, err := r.HandleMatrixMessage(context.Background(), msg); err == nil || resp != nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("upload rejection was not surfaced: %v", err)
	}
}

func TestMediaErrorsDoNotExposeCapabilityURLs(t *testing.T) {
	err := mediaError(context.Background(), "media transfer", errors.New("Get https://storage.example/private?token=secret: failure"))
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "storage.example") {
		t.Fatal("capability leaked through error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(mediaError(ctx, "download", errors.New("failed")), context.Canceled) {
		t.Fatal("cancellation was hidden")
	}
}

func encryptedCopy(key *attachment.EncryptedFile, plain []byte) []byte {
	encrypted := bytes.Clone(plain)
	key.EncryptInPlace(encrypted)
	return encrypted
}
