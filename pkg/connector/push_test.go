package connector

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"go.mau.fi/util/dbutil"
	_ "go.mau.fi/util/dbutil/litestream"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"

	"github.com/beeper/reddit/pkg/redditchat"
)

type pushTransport func(*http.Request) (*http.Response, error)

func (f pushTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestPushRegistrationFromWrapper(t *testing.T) {
	r := testClient(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected Matrix request") })
	r.meta.PushDeviceID = "persistent-bridge-device"
	calls := 0
	r.matrix().Client.Transport = pushTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		var body struct {
			Variables struct {
				PushToken string `json:"pushToken"`
				DeviceID  string `json:"deviceId"`
			} `json:"variables"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		wantToken := "first-token"
		if calls == 2 {
			wantToken = "rotated-token"
		}
		if body.Variables.PushToken != wantToken || body.Variables.DeviceID != "persistent-bridge-device" {
			t.Fatal("wrong token or device changed on token rotation")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":{"registerMobilePushToken":{"ok":true}}}`))}, nil
	})
	var pushable bridgev2.PushableNetworkAPI = r
	cfg := pushable.GetPushConfigs()
	if cfg.FCM == nil || cfg.FCM.SenderID != "933360113277" || cfg.Web != nil || cfg.APNs != nil || cfg.Native {
		t.Fatalf("wrong relay configuration: %+v", cfg)
	}
	for _, token := range []string{"first-token", "rotated-token"} {
		if err := pushable.RegisterPushNotifications(t.Context(), bridgev2.PushTypeFCM, token); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("got %d registrations", calls)
	}
}

func TestPushRegistrationRejectsUnsupportedRequests(t *testing.T) {
	r := testClient(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected request") })
	for _, pushType := range []bridgev2.PushType{bridgev2.PushTypeUnknown, bridgev2.PushTypeWeb, bridgev2.PushTypeAPNs} {
		if err := r.RegisterPushNotifications(t.Context(), pushType, "token"); err == nil {
			t.Fatalf("accepted unsupported type: %s", pushType)
		}
	}
	if err := r.RegisterPushNotifications(t.Context(), bridgev2.PushTypeFCM, " "); err == nil {
		t.Fatal("accepted blank token")
	}
	r.rc = nil
	if err := r.RegisterPushNotifications(t.Context(), bridgev2.PushTypeFCM, "token"); !errors.Is(err, bridgev2.ErrNotLoggedIn) {
		t.Fatalf("wrong missing-login error: %v", err)
	}
}

func TestPushRegistrationWithoutRenewableSession(t *testing.T) {
	r := testClient(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected Matrix request") })
	r.meta.PushDeviceID = "device"
	calls := 0
	r.matrix().Client.Transport = pushTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})
	err := r.RegisterPushNotifications(t.Context(), bridgev2.PushTypeFCM, "token")
	if !errors.Is(err, redditchat.ErrPushAuthentication) || calls != 1 {
		t.Fatalf("wrong auth failure: %v (%d calls)", err, calls)
	}
}

func pushTestDatabase(t *testing.T, r *RedditClient) {
	t.Helper()
	db, err := dbutil.NewWithDialect(filepath.Join(t.TempDir(), "push.db"), "sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	r.main.Bridge.DB = database.New("", r.main.GetDBMetaTypes(), db)
	if err = db.Upgrade(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.userLogin.Bridge = r.main.Bridge
	r.userLogin.Metadata = r.meta
	r.userLogin.UserMXID = "@test:example.com"
	if err = r.main.Bridge.DB.User.Insert(t.Context(), &database.User{MXID: r.userLogin.UserMXID}); err != nil {
		t.Fatal(err)
	}
	if err = r.main.Bridge.DB.UserLogin.Insert(t.Context(), r.userLogin.UserLogin); err != nil {
		t.Fatal(err)
	}
}

func TestPushDeviceIDPersistenceAndSaveFailure(t *testing.T) {
	r := testClient(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected request") })
	pushTestDatabase(t, r)
	deviceID, err := r.pushDeviceID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = uuid.Parse(deviceID); err != nil {
		t.Fatal(err)
	}
	stored, err := r.main.Bridge.DB.UserLogin.GetByID(t.Context(), r.userLogin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Metadata.(*UserLoginMetadata).PushDeviceID != deviceID {
		t.Fatal("push device ID was not persisted")
	}
	if err = r.main.Bridge.DB.Close(); err != nil {
		t.Fatal(err)
	}
	if reused, err := r.pushDeviceID(t.Context()); err != nil || reused != deviceID {
		t.Fatalf("existing device ID was not reused: %v", err)
	}
	r.meta.PushDeviceID = ""
	err = r.RegisterPushNotifications(t.Context(), bridgev2.PushTypeFCM, "token")
	if err == nil || r.meta.PushDeviceID != "" {
		t.Fatal("failed save retained an unpersisted device ID")
	}
}

func TestPushRegistrationRefreshesExpiredCredentials(t *testing.T) {
	r := testClient(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected request") })
	pushTestDatabase(t, r)
	freshToken := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"lid":"t2_a"}`)) + ".signature"
	registrationCalls, refreshCalls := 0, 0
	transport := pushTransport(func(req *http.Request) (*http.Response, error) {
		status, body := http.StatusOK, ""
		switch req.URL.Host + req.URL.Path {
		case "gql-fed.reddit.com/":
			registrationCalls++
			if registrationCalls == 1 {
				status, body = http.StatusUnauthorized, `{}`
			} else {
				if req.Header.Get("Authorization") != "Bearer "+freshToken {
					t.Fatal("registration retry used expired credentials")
				}
				body = `{"data":{"registerMobilePushToken":{"ok":true}}}`
			}
		case "www.reddit.com/svc/shreddit/token":
			refreshCalls++
			body = `{"token":"` + freshToken + `","expires":1900000000}`
		case "matrix.redditspace.com/_matrix/client/v3/account/whoami":
			body = `{"device_id":"matrix-device"}`
		default:
			t.Fatalf("unexpected request: %s", req.URL)
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	previousTransport := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	r.matrix().Client.Transport = transport
	var err error
	r.session, err = redditchat.NewRedditSession(&http.Client{Transport: transport}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	r.session.CSRFToken = "csrf"
	if err = r.RegisterPushNotifications(t.Context(), bridgev2.PushTypeFCM, "push-token"); err != nil {
		t.Fatal(err)
	}
	if registrationCalls != 2 || refreshCalls != 1 || r.meta.Credentials.AccessToken != freshToken {
		t.Fatalf("incorrect refresh: registrations=%d refreshes=%d", registrationCalls, refreshCalls)
	}
}
