package redditchat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
)

const (
	DefaultRedditURL       = "https://www.reddit.com"
	DefaultRedditUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/148.0.0.0 Safari/537.36"

	RedditLoginCaptchaSiteKey = "6LfirrMoAAAAAHZOipvza4kpp_VtTwLNuXVwURNQ"
	RedditLoginCaptchaAction  = "v1/web/login_with_password"

	// DefaultRedditClientVersion is the X-Reddit-Client-Version header value
	DefaultRedditClientVersion = "2026-06-24T12:00Z~unknown"
)

type CaptchaStep string

const (
	CaptchaStepPassword CaptchaStep = "password"
	CaptchaStepOTP      CaptchaStep = "otp"
)

var (
	ErrBrowserVerificationBlocked = errors.New("reddit login: browser verification blocked")
	ErrSessionUnavailable         = errors.New("reddit login: saved session is unavailable")
	ErrInvalidCredentials         = errors.New("reddit login: username or password rejected")
	ErrInvalidOTP                 = errors.New("reddit login: otp code rejected")
	ErrSSORequired                = errors.New("reddit login: account requires SSO login")
)

type CaptchaRequest struct {
	SiteKey string
	Action  string
	PageURL string
	Step    CaptchaStep
}

type CaptchaResult struct {
	Token         string
	ClientVersion string
}

func (r CaptchaRequest) EnterpriseExecuteJavaScript() string {
	siteKey, _ := json.Marshal(firstNonEmpty(r.SiteKey, RedditLoginCaptchaSiteKey))
	action, _ := json.Marshal(firstNonEmpty(r.Action, RedditLoginCaptchaAction))
	return fmt.Sprintf(`(async () => {
	const siteKey = %s;
	const action = %s;
	if (!window.grecaptcha || !window.grecaptcha.enterprise) {
		await new Promise((resolve, reject) => {
			const script = document.createElement("script");
			script.src = "https://www.google.com/recaptcha/enterprise.js?render=" + encodeURIComponent(siteKey);
			script.async = true;
			script.onload = resolve;
			script.onerror = () => reject(new Error("failed to load reCAPTCHA Enterprise"));
			document.head.appendChild(script);
		});
	}
	await new Promise(resolve => window.grecaptcha.enterprise.ready(resolve));
	return await window.grecaptcha.enterprise.execute(siteKey, { action });
})()`, siteKey, action)
}

type RedditSession struct {
	Client    *http.Client
	BaseURL   string
	UserAgent string
	CSRFToken string
	// ClientVersion is the X-Reddit-Client-Version sniffed from the login
	// webview. Empty until the first captcha step completes.
	ClientVersion string
}

func (s *RedditSession) clientVersion() string {
	if s.ClientVersion != "" {
		return s.ClientVersion
	}
	return DefaultRedditClientVersion
}

type RedditChatToken struct {
	Token   string `json:"token"`
	Expires int64  `json:"expires"`
}

func NewRedditSession(httpClient *http.Client, baseURL, userAgent string) (*RedditSession, error) {
	if httpClient == nil {
		jar, err := cookiejar.New(nil)
		if err != nil {
			return nil, err
		}
		httpClient = &http.Client{Jar: jar}
	}
	if httpClient.Jar == nil {
		jar, err := cookiejar.New(nil)
		if err != nil {
			return nil, err
		}
		httpClient.Jar = jar
	}
	if baseURL == "" {
		baseURL = DefaultRedditURL
	}
	if userAgent == "" {
		userAgent = DefaultRedditUserAgent
	}
	return &RedditSession{
		Client:    httpClient,
		BaseURL:   strings.TrimRight(baseURL, "/"),
		UserAgent: userAgent,
	}, nil
}

func CredentialsFromRedditSession(ctx context.Context, session *RedditSession) (RedditChatToken, Credentials, error) {
	token, err := session.RefreshChatToken(ctx)
	if err != nil {
		return RedditChatToken{}, Credentials{}, err
	}
	creds, err := credentialsFromChatToken(token.Token)
	if err != nil {
		return RedditChatToken{}, Credentials{}, err
	}
	if deviceID, err := matrixWhoamiDevice(ctx, session.Client, token.Token); err == nil {
		creds.DeviceID = deviceID
	}
	return token, creds, nil
}

func (s *RedditSession) RefreshChatToken(ctx context.Context) (RedditChatToken, error) {
	if s == nil {
		return RedditChatToken{}, ErrSessionUnavailable
	}
	csrf := s.csrfToken()
	if csrf == "" {
		return RedditChatToken{}, fmt.Errorf("%w: missing csrf_token cookie", ErrSessionUnavailable)
	}
	form := url.Values{"csrf_token": {csrf}}
	req, err := s.newRequest(ctx, http.MethodPost, "/svc/shreddit/token", strings.NewReader(form.Encode()))
	if err != nil {
		return RedditChatToken{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", s.BaseURL)
	req.Header.Set("Referer", s.BaseURL+"/chat/")
	req.Header.Set("X-Original-Referer", s.BaseURL+"/chat/")
	req.Header.Set("X-Reddit-Client-Version", s.clientVersion())
	var token RedditChatToken
	if err := s.doJSON(req, &token); err != nil {
		return RedditChatToken{}, err
	}
	if token.Token == "" {
		return RedditChatToken{}, errors.New("reddit login: chat token response did not include token")
	}
	return token, nil
}

func (s *RedditSession) prepareLogin(ctx context.Context) (string, error) {
	req, err := s.newRequest(ctx, http.MethodGet, "/login/", nil)
	if err != nil {
		return "", err
	}
	resp, body, err := s.do(req)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", redditStatusError(resp, body)
	}
	text := string(body)
	if strings.Contains(text, "Please wait for verification") || findFirst(text, `name=["'](js_challenge)["']`) != "" {
		nextPath, err := solveJSChallenge(text)
		if err != nil {
			return "", err
		}
		req, err = s.newRequest(ctx, http.MethodGet, nextPath, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Referer", s.BaseURL+"/login/")
		resp, body, err = s.do(req)
		if err != nil {
			return "", err
		}
		if resp.StatusCode != http.StatusOK {
			return "", redditStatusError(resp, body)
		}
		text = string(body)
	}
	if strings.Contains(text, "You've been blocked by network security") {
		return "", ErrBrowserVerificationBlocked
	}
	s.CSRFToken = s.csrfToken()
	if s.CSRFToken == "" {
		s.CSRFToken = findFirst(text, `csrf_token":"([^"]+)`, `csrf_token&quot;:&quot;([^&]+)`, `CSRF":"([^"]+)`)
	}
	if s.CSRFToken == "" {
		return "", errors.New("reddit login: csrf_token not found after login page load")
	}
	if renderID := findFirst(text, `serverRenderId="([^"]+)`, `renderId=([a-f0-9-]+)`); renderID != "" {
		_ = s.updateRecaptcha(ctx, "login", "initial", renderID)
	}
	return text, nil
}

func (s *RedditSession) checkOIDCRequired(ctx context.Context, username string) error {
	body, err := json.Marshal(map[string]string{
		"userIdentifier": username,
		"csrf_token":     s.csrfToken(),
	})
	if err != nil {
		return err
	}
	req, err := s.newRequest(ctx, http.MethodPost, "/svc/shreddit/account/login/check_is_oidc_required", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", s.BaseURL)
	req.Header.Set("Referer", s.BaseURL+"/login/")
	var resp struct {
		IsSSO bool `json:"is_sso"`
	}
	if err := s.doJSON(req, &resp); err != nil {
		return err
	}
	if resp.IsSSO {
		return ErrSSORequired
	}
	return nil
}

// PrepareLogin initializes the session once; subsequent CAPTCHA and OTP steps
// continue with the same cookies and CSRF token.
func (s *RedditSession) PrepareLogin(ctx context.Context, username string) error {
	if _, err := s.prepareLogin(ctx); err != nil {
		return err
	}
	return s.checkOIDCRequired(ctx, username)
}

func (s *RedditSession) CaptchaRequest(step CaptchaStep) CaptchaRequest {
	return CaptchaRequest{SiteKey: RedditLoginCaptchaSiteKey, Action: RedditLoginCaptchaAction,
		PageURL: s.BaseURL + "/login/", Step: step}
}

func (s *RedditSession) loginForm(username, password string, captcha CaptchaResult) url.Values {
	if captcha.ClientVersion != "" {
		s.ClientVersion = captcha.ClientVersion
	}
	return url.Values{
		"username": {username}, "password": {password},
		"recaptcha_token": {captcha.Token}, "recaptcha_use_checkbox": {"false"},
		"recaptcha_action": {RedditLoginCaptchaAction}, "csrf_token": {s.csrfToken()},
	}
}

// SubmitPassword reports whether Reddit requires an OTP. It does not restart
// authentication or wait for user input: bridgev2 owns the intervening steps.
func (s *RedditSession) SubmitPassword(ctx context.Context, username, password string, captcha CaptchaResult) (needsOTP bool, err error) {
	resp, body, err := s.postLoginForm(ctx, "/svc/shreddit/account/login", s.loginForm(username, password, captcha))
	if err != nil {
		return false, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return false, checkLoginResponseBody(body, ErrInvalidCredentials)
	case http.StatusAccepted:
		return true, nil
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		return false, fmt.Errorf("%w: %w", ErrInvalidCredentials, redditStatusError(resp, body))
	default:
		return false, redditStatusError(resp, body)
	}
}

func (s *RedditSession) SubmitOTP(ctx context.Context, username, password, otp string, captcha CaptchaResult) error {
	form := s.loginForm(username, password, captcha)
	form.Set("appOtp", otp)
	resp, body, err := s.postLoginForm(ctx, "/svc/shreddit/account/login/otp", form)
	if err != nil {
		return err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return checkLoginResponseBody(body, ErrInvalidOTP)
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: %w", ErrInvalidOTP, redditStatusError(resp, body))
	default:
		return redditStatusError(resp, body)
	}
}

func (s *RedditSession) postLoginForm(ctx context.Context, path string, form url.Values) (*http.Response, []byte, error) {
	req, err := s.newRequest(ctx, http.MethodPost, path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", s.BaseURL)
	req.Header.Set("Referer", s.BaseURL+"/login/")
	req.Header.Set("X-Original-Referer", s.BaseURL+"/login/")
	req.Header.Set("X-Reddit-Client-Version", s.clientVersion())
	return s.do(req)
}

func (s *RedditSession) updateRecaptcha(ctx context.Context, pageType, phase, renderID string) error {
	key := base64.RawURLEncoding.EncodeToString([]byte(pageType + "|" + phase + "|" + renderID))
	req, err := s.newRequest(ctx, http.MethodGet, "/svc/shreddit/update-recaptcha?k="+key, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Referer", s.BaseURL+"/login/")
	resp, body, err := s.do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return redditStatusError(resp, body)
	}
	return nil
}

func (s *RedditSession) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	u := path
	if strings.HasPrefix(path, "/") {
		u = s.BaseURL + path
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", s.UserAgent)
	req.Header.Set("Accept", "application/json,text/plain,*/*")
	return req, nil
}

func (s *RedditSession) doJSON(req *http.Request, out any) error {
	resp, body, err := s.do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return redditStatusError(resp, body)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("reddit login: decode response: %w", err)
	}
	return nil
}

func (s *RedditSession) do(req *http.Request) (*http.Response, []byte, error) {
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	return resp, body, nil
}

func (s *RedditSession) csrfToken() string {
	if s.CSRFToken != "" {
		return s.CSRFToken
	}
	u, err := url.Parse(s.BaseURL)
	if err != nil || s.Client == nil || s.Client.Jar == nil {
		return ""
	}
	for _, cookie := range s.Client.Jar.Cookies(u) {
		if cookie.Name == "csrf_token" {
			s.CSRFToken = cookie.Value
			return cookie.Value
		}
	}
	return ""
}

func solveJSChallenge(text string) (string, error) {
	seed := findFirst(text, `await\(async e=>e\+e\)\("([^"]+)"\)`)
	tokenName := "jsc_token"
	token := findFirst(text, `name="jsc_token" value="([^"]+)"`)
	if token == "" {
		tokenName = "token"
		token = findFirst(text, `name="token" value="([^"]+)"`)
	}
	action := findFirst(text, `<form[^>]+action="([^"]+)"`)
	if seed == "" || token == "" {
		return "", errors.New("reddit login: javascript verification challenge not found")
	}
	if action == "" {
		action = "/login/"
	}
	values := url.Values{
		"solution":     {seed + seed},
		"js_challenge": {"1"},
		tokenName:      {token},
		"jsc_orig_r":   {findFirst(text, `name="jsc_orig_r" value="([^"]*)"`)},
	}
	return action + "?" + values.Encode(), nil
}

func credentialsFromChatToken(token string) (Credentials, error) {
	payload, err := jwtPayload(token)
	if err != nil {
		return Credentials{}, err
	}
	var claims struct {
		LoggedInID string `json:"lid"`
		AccountID  string `json:"aid"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Credentials{}, fmt.Errorf("reddit login: decode chat token claims: %w", err)
	}
	user := firstNonEmpty(claims.LoggedInID, claims.AccountID)
	if user == "" {
		return Credentials{}, errors.New("reddit login: chat token did not include reddit user ID")
	}
	return Credentials{
		Homeserver:  DefaultHomeserver,
		AccessToken: token,
		UserID:      "@" + user + ":reddit.com",
	}, nil
}

func jwtPayload(token string) ([]byte, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil, errors.New("reddit login: malformed JWT token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("reddit login: decode JWT payload: %w", err)
	}
	return payload, nil
}

func matrixWhoamiDevice(ctx context.Context, httpClient *http.Client, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, DefaultHomeserver+"/_matrix/client/v3/account/whoami", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", DefaultRedditUserAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", redditStatusError(resp, body)
	}
	var whoami struct {
		DeviceID string `json:"device_id"`
	}
	if err := json.Unmarshal(body, &whoami); err != nil {
		return "", err
	}
	return whoami.DeviceID, nil
}

func checkLoginResponseBody(body []byte, rejected error) error {
	var parsed struct {
		Success *bool           `json:"success"`
		Error   json.RawMessage `json:"error"`
		Reason  json.RawMessage `json:"reason"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		// Non-JSON 200 (e.g. empty body) — nothing to fail on here.
		return nil
	}
	failed := (parsed.Success != nil && !*parsed.Success) ||
		(len(parsed.Error) > 0 && string(parsed.Error) != "null") ||
		(len(parsed.Reason) > 0 && string(parsed.Reason) != "null")
	if failed {
		return rejected
	}
	return nil
}

type LoginHTTPError struct {
	Operation  string
	StatusCode int
}

func (e *LoginHTTPError) Error() string {
	return fmt.Sprintf("reddit login: %s request failed: status=%d", e.Operation, e.StatusCode)
}

func redditStatusError(resp *http.Response, body []byte) error {
	// Authentication responses, redirect URLs and headers may contain session material.
	// Keep errors safe to forward to bridge logs and client login notices.
	operation := "sign-in"
	if resp.Request != nil && resp.Request.URL != nil {
		switch resp.Request.URL.Path {
		case "/svc/shreddit/account/login/otp":
			operation = "two-factor verification"
		case "/svc/shreddit/token":
			operation = "chat session"
		case "/svc/shreddit/account/login/check_is_oidc_required":
			operation = "account lookup"
		case "/svc/shreddit/update-recaptcha":
			operation = "verification setup"
		}
	}
	return &LoginHTTPError{Operation: operation, StatusCode: resp.StatusCode}
}

func findFirst(text string, patterns ...string) string {
	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		match := re.FindStringSubmatch(text)
		if len(match) > 1 {
			return html.UnescapeString(match[1])
		}
	}
	return ""
}
