package redditchat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

type storageState struct {
	Cookies []struct {
		Name     string  `json:"name"`
		Value    string  `json:"value"`
		Domain   string  `json:"domain"`
		Path     string  `json:"path"`
		Expires  float64 `json:"expires"`
		HTTPOnly bool    `json:"httpOnly"`
		Secure   bool    `json:"secure"`
		SameSite string  `json:"sameSite"`
	} `json:"cookies"`
	Origins []struct {
		Origin       string `json:"origin"`
		LocalStorage []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"localStorage"`
	} `json:"origins"`
}

func CredentialsFromPlaywrightStorageState(path string) (Credentials, error) {
	state, err := readStorageState(path)
	if err != nil {
		return Credentials{}, err
	}
	for _, origin := range state.Origins {
		if origin.Origin != "https://www.reddit.com" {
			continue
		}
		kv := make(map[string]string, len(origin.LocalStorage))
		for _, item := range origin.LocalStorage {
			kv[item.Name] = item.Value
		}
		return credentialsFromLocalStorage(kv)
	}
	return Credentials{}, errors.New("reddit chat: https://www.reddit.com localStorage not found in storage state")
}

func SeedChromeFromPlaywrightStorageState(ctx context.Context, debugURL, statePath string) error {
	state, err := readStorageState(statePath)
	if err != nil {
		return err
	}
	localStorage := map[string]string{}
	for _, origin := range state.Origins {
		if origin.Origin != "https://www.reddit.com" {
			continue
		}
		for _, item := range origin.LocalStorage {
			localStorage[item.Name] = item.Value
		}
		break
	}
	if len(localStorage) == 0 {
		return errors.New("reddit chat: no reddit localStorage entries found to seed")
	}
	allocCtx, cancel := chromedp.NewRemoteAllocator(ctx, debugURL)
	defer cancel()
	tabCtx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	cookies := make([]*network.CookieParam, 0, len(state.Cookies))
	for _, cookie := range state.Cookies {
		if cookie.Domain == "" {
			continue
		}
		param := &network.CookieParam{
			Name:     cookie.Name,
			Value:    cookie.Value,
			Domain:   cookie.Domain,
			Path:     cookie.Path,
			Secure:   cookie.Secure,
			HTTPOnly: cookie.HTTPOnly,
		}
		switch cookie.SameSite {
		case "Strict":
			param.SameSite = network.CookieSameSiteStrict
		case "Lax":
			param.SameSite = network.CookieSameSiteLax
		case "None":
			param.SameSite = network.CookieSameSiteNone
		}
		if cookie.Expires > 0 {
			expires := cdp.TimeSinceEpoch(time.Unix(int64(cookie.Expires), 0))
			param.Expires = &expires
		}
		cookies = append(cookies, param)
	}
	localStorageJSON, err := json.Marshal(localStorage)
	if err != nil {
		return err
	}
	actions := []chromedp.Action{network.Enable()}
	if len(cookies) > 0 {
		actions = append(actions, network.SetCookies(cookies))
	}
	actions = append(actions,
		chromedp.Navigate("https://www.reddit.com/"),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Evaluate(fmt.Sprintf(`(() => {
			const items = %s;
			for (const [key, value] of Object.entries(items)) localStorage.setItem(key, value);
			return true;
		})()`, string(localStorageJSON)), nil),
	)
	return chromedp.Run(tabCtx, actions...)
}

func CredentialsFromChromeDebugURL(ctx context.Context, debugURL string) (Credentials, error) {
	if debugURL == "" {
		debugURL = "http://127.0.0.1:9222"
	}
	allocCtx, cancel := chromedp.NewRemoteAllocator(ctx, debugURL)
	defer cancel()
	tabCtx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	localStorage := map[string]string{}
	err := chromedp.Run(tabCtx,
		chromedp.Navigate(DefaultChatURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(5*time.Second),
		chromedp.Evaluate(`(() => {
			const keys = [
				"chat:matrix-user-id",
				"chat:matrix-device-id",
				"chat:matrix-access-token",
				"chat:access-token"
			];
			const out = {};
			for (const key of keys) out[key] = localStorage.getItem(key) || "";
			return out;
		})()`, &localStorage),
	)
	if err != nil {
		return Credentials{}, err
	}
	return credentialsFromLocalStorage(localStorage)
}

func CaptchaTokenProviderFromChromeDebugURL(debugURL string) CaptchaTokenProvider {
	return func(ctx context.Context, req CaptchaRequest) (string, error) {
		return CaptchaTokenFromChromeDebugURL(ctx, debugURL, req)
	}
}

func CaptchaTokenFromChromeDebugURL(ctx context.Context, debugURL string, req CaptchaRequest) (string, error) {
	if debugURL == "" {
		debugURL = "http://127.0.0.1:9222"
	}
	if req.SiteKey == "" {
		req.SiteKey = RedditLoginCaptchaSiteKey
	}
	if req.Action == "" {
		req.Action = RedditLoginCaptchaAction
	}
	if req.PageURL == "" {
		req.PageURL = DefaultRedditURL + "/login/"
	}
	allocCtx, cancel := chromedp.NewRemoteAllocator(ctx, debugURL)
	defer cancel()
	tabCtx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	var token string
	err := chromedp.Run(tabCtx,
		chromedp.Navigate(req.PageURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(2*time.Second),
		chromedp.ActionFunc(func(ctx context.Context) error {
			var blocked bool
			if err := chromedp.Evaluate(`document.body && document.body.innerText.includes("You've been blocked by network security")`, &blocked).Do(ctx); err != nil {
				return err
			}
			if blocked {
				return ErrBrowserVerificationBlocked
			}
			return nil
		}),
		chromedp.Evaluate(req.EnterpriseExecuteJavaScript(), &token),
	)
	if err != nil {
		return "", err
	}
	if token == "" {
		return "", errors.New("reddit login: browser reCAPTCHA did not return a token")
	}
	return token, nil
}

func readStorageState(path string) (storageState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return storageState{}, err
	}
	var state storageState
	if err := json.Unmarshal(data, &state); err != nil {
		return storageState{}, err
	}
	return state, nil
}

func credentialsFromLocalStorage(kv map[string]string) (Credentials, error) {
	userID := parseJSONString(kv["chat:matrix-user-id"])
	deviceID := parseJSONString(kv["chat:matrix-device-id"])
	token := parseJSONString(kv["chat:matrix-access-token"])
	if token == "" && kv["chat:access-token"] != "" {
		var tokenObj struct {
			Token string `json:"token"`
		}
		_ = json.Unmarshal([]byte(kv["chat:access-token"]), &tokenObj)
		token = tokenObj.Token
	}
	if userID == "" {
		return Credentials{}, fmt.Errorf("reddit chat: Matrix user ID missing from localStorage")
	}
	if token == "" {
		return Credentials{}, fmt.Errorf("reddit chat: Matrix access token missing from localStorage")
	}
	return Credentials{
		Homeserver:  DefaultHomeserver,
		AccessToken: token,
		UserID:      userID,
		DeviceID:    deviceID,
	}, nil
}

func parseJSONString(raw string) string {
	if raw == "" {
		return ""
	}
	var parsed string
	if json.Unmarshal([]byte(raw), &parsed) == nil {
		return parsed
	}
	return raw
}
