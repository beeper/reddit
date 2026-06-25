package redditchat

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
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
	"sync"
	"time"

	"github.com/rs/zerolog"
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
	ErrCaptchaRequired            = errors.New("reddit login: captcha token provider required")
	ErrBrowserVerificationBlocked = errors.New("reddit login: browser verification blocked")
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

type CaptchaTokenProvider func(ctx context.Context, req CaptchaRequest) (CaptchaResult, error)

func (r CaptchaRequest) EnterpriseScriptURL() string {
	return "https://www.google.com/recaptcha/enterprise.js?render=" + url.QueryEscape(firstNonEmpty(r.SiteKey, RedditLoginCaptchaSiteKey))
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

type CaptchaRequiredError struct {
	Request CaptchaRequest
}

func (e *CaptchaRequiredError) Error() string {
	return fmt.Sprintf("reddit login: captcha token required for site_key=%s action=%s page=%s", e.Request.SiteKey, e.Request.Action, e.Request.PageURL)
}

func (e *CaptchaRequiredError) Unwrap() error {
	return ErrCaptchaRequired
}

type RedditLoginOptions struct {
	Username             string
	Password             string
	TOTPCode             string
	TOTPSecret           string
	CaptchaTokenProvider CaptchaTokenProvider
	HTTPClient           *http.Client
	UserAgent            string
	BaseURL              string
}

type RedditLoginResult struct {
	Session     *RedditSession
	Credentials Credentials
	Token       RedditChatToken
}

type RedditSession struct {
	Client    *http.Client
	BaseURL   string
	UserAgent string
	CSRFToken string
	// ClientVersion is the X-Reddit-Client-Version sniffed from the login
	// webview (see captchaToken). Empty until the first captcha step completes.
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

func LoginReddit(ctx context.Context, opts RedditLoginOptions) (*RedditLoginResult, error) {
	if opts.Username == "" {
		return nil, errors.New("reddit login: missing username")
	}
	if opts.Password == "" {
		return nil, errors.New("reddit login: missing password")
	}
	session, err := NewRedditSession(opts.HTTPClient, opts.BaseURL, opts.UserAgent)
	if err != nil {
		return nil, err
	}
	loginPage, err := session.prepareLogin(ctx)
	if err != nil {
		return nil, err
	}
	if err := session.checkOIDCRequired(ctx, opts.Username, loginPage); err != nil {
		return nil, err
	}
	if err := session.submitPassword(ctx, opts, loginPage); err != nil {
		return nil, err
	}
	token, creds, err := CredentialsFromRedditSession(ctx, session)
	if err != nil {
		return nil, err
	}
	return &RedditLoginResult{
		Session:     session,
		Credentials: creds,
		Token:       token,
	}, nil
}

func NewFromRedditLogin(ctx context.Context, opts RedditLoginOptions) (*Client, *RedditSession, error) {
	result, err := LoginReddit(ctx, opts)
	if err != nil {
		return nil, nil, err
	}
	client, err := New(result.Credentials)
	if err != nil {
		return nil, nil, err
	}
	return client, result.Session, nil
}

func StaticCaptchaTokenProvider(tokens ...string) CaptchaTokenProvider {
	var mu sync.Mutex
	var next int
	return func(ctx context.Context, req CaptchaRequest) (CaptchaResult, error) {
		mu.Lock()
		defer mu.Unlock()
		if next >= len(tokens) {
			return CaptchaResult{}, &CaptchaRequiredError{Request: req}
		}
		token := strings.TrimSpace(tokens[next])
		next++
		if token == "" {
			return CaptchaResult{}, errors.New("reddit login: empty captcha token")
		}
		return CaptchaResult{Token: token}, nil
	}
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

func RedditSessionFromPlaywrightStorageState(path string) (*RedditSession, error) {
	state, err := readStorageState(path)
	if err != nil {
		return nil, err
	}
	session, err := NewRedditSession(nil, "", "")
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(session.BaseURL)
	if err != nil {
		return nil, err
	}
	cookies := make([]*http.Cookie, 0, len(state.Cookies))
	now := time.Now()
	for _, item := range state.Cookies {
		if !strings.Contains(item.Domain, "reddit.com") {
			continue
		}
		cookie := &http.Cookie{
			Name:     item.Name,
			Value:    item.Value,
			Path:     item.Path,
			Domain:   item.Domain,
			Secure:   item.Secure,
			HttpOnly: item.HTTPOnly,
		}
		if item.Expires > 0 {
			cookie.Expires = time.Unix(int64(item.Expires), 0)
			if cookie.Expires.Before(now) {
				continue
			}
		}
		if !validCookieValue(cookie.Value) {
			continue
		}
		cookies = append(cookies, cookie)
		if item.Name == "csrf_token" {
			session.CSRFToken = item.Value
		}
	}
	session.Client.Jar.SetCookies(u, cookies)
	return session, nil
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
		return RedditChatToken{}, errors.New("reddit login: nil session")
	}
	csrf := s.csrfToken()
	if csrf == "" {
		return RedditChatToken{}, errors.New("reddit login: missing csrf_token cookie")
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
	// Diagnostic: confirm the session looks authenticated before the token POST.
	log := zerolog.Ctx(ctx)
	if log != nil {
		var cookieNames []string
		if u, perr := url.Parse(s.BaseURL); perr == nil && s.Client != nil && s.Client.Jar != nil {
			for _, c := range s.Client.Jar.Cookies(u) {
				cookieNames = append(cookieNames, c.Name)
			}
		}
		log.Debug().
			Bool("has_csrf", csrf != "").
			Strs("cookie_names", cookieNames).
			Msg("Refreshing reddit chat token")
	}
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
	if strings.Contains(text, "Please wait for verification") {
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

func (s *RedditSession) checkOIDCRequired(ctx context.Context, username, loginPage string) error {
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
		return errors.New("reddit login: account requires SSO login")
	}
	return nil
}

func (s *RedditSession) submitPassword(ctx context.Context, opts RedditLoginOptions, loginPage string) error {
	captcha, err := opts.captchaToken(ctx, s.BaseURL, CaptchaStepPassword)
	if err != nil {
		return err
	}
	if captcha.ClientVersion != "" {
		s.ClientVersion = captcha.ClientVersion
	}
	form := url.Values{
		"username":               {opts.Username},
		"password":               {opts.Password},
		"recaptcha_token":        {captcha.Token},
		"recaptcha_use_checkbox": {"false"},
		"recaptcha_action":       {RedditLoginCaptchaAction},
		"csrf_token":             {s.csrfToken()},
	}
	resp, body, err := s.postLoginForm(ctx, "/svc/shreddit/account/login", form)
	if err != nil {
		return err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return checkLoginResponseBody(ctx, body)
	case http.StatusAccepted:
		otp, err := opts.otpCode(time.Now())
		if err != nil {
			return err
		}
		captcha, err = opts.captchaToken(ctx, s.BaseURL, CaptchaStepOTP)
		if err != nil {
			return err
		}
		if captcha.ClientVersion != "" {
			s.ClientVersion = captcha.ClientVersion
		}
		form := url.Values{
			"appOtp":                 {otp},
			"recaptcha_token":        {captcha.Token},
			"recaptcha_use_checkbox": {"false"},
			"recaptcha_action":       {RedditLoginCaptchaAction},
			"username":               {opts.Username},
			"password":               {opts.Password},
			"csrf_token":             {s.csrfToken()},
		}
		resp, body, err := s.postLoginForm(ctx, "/svc/shreddit/account/login/otp", form)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusOK {
			return redditStatusError(resp, body)
		}
		return nil
	default:
		return redditStatusError(resp, body)
	}
}

func (opts RedditLoginOptions) captchaToken(ctx context.Context, baseURL string, step CaptchaStep) (CaptchaResult, error) {
	req := CaptchaRequest{
		SiteKey: RedditLoginCaptchaSiteKey,
		Action:  RedditLoginCaptchaAction,
		PageURL: strings.TrimRight(firstNonEmpty(baseURL, DefaultRedditURL), "/") + "/login/",
		Step:    step,
	}
	if opts.CaptchaTokenProvider == nil {
		return CaptchaResult{}, &CaptchaRequiredError{Request: req}
	}
	result, err := opts.CaptchaTokenProvider(ctx, req)
	if err != nil {
		return CaptchaResult{}, err
	}
	result.Token = strings.TrimSpace(result.Token)
	if result.Token == "" {
		return CaptchaResult{}, errors.New("reddit login: empty captcha token")
	}
	return result, nil
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
		return fmt.Errorf("reddit login: decode %s: %w", req.URL.String(), err)
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

func (opts RedditLoginOptions) otpCode(now time.Time) (string, error) {
	if opts.TOTPCode != "" {
		return opts.TOTPCode, nil
	}
	if opts.TOTPSecret == "" {
		return "", errors.New("reddit login: otp required; provide TOTPCode or TOTPSecret")
	}
	return GenerateTOTP(opts.TOTPSecret, now)
}

func GenerateTOTP(secret string, now time.Time) (string, error) {
	key, err := decodeBase32Secret(secret)
	if err != nil {
		return "", err
	}
	counter := uint64(now.Unix() / 30)
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	sum := hmac.New(sha1.New, key)
	_, _ = sum.Write(msg[:])
	hash := sum.Sum(nil)
	offset := hash[len(hash)-1] & 0x0f
	code := (int(hash[offset])&0x7f)<<24 |
		(int(hash[offset+1])&0xff)<<16 |
		(int(hash[offset+2])&0xff)<<8 |
		(int(hash[offset+3]) & 0xff)
	return fmt.Sprintf("%06d", code%1000000), nil
}

func decodeBase32Secret(secret string) ([]byte, error) {
	cleaned := strings.ToUpper(strings.TrimSpace(secret))
	cleaned = strings.TrimRight(cleaned, "=")
	if cleaned == "" {
		return nil, errors.New("reddit login: empty TOTP secret")
	}
	if rem := len(cleaned) % 8; rem != 0 {
		cleaned += strings.Repeat("=", 8-rem)
	}
	key, err := base32.StdEncoding.DecodeString(cleaned)
	if err != nil {
		return nil, fmt.Errorf("reddit login: decode TOTP secret: %w", err)
	}
	return key, nil
}

func solveJSChallenge(text string) (string, error) {
	seed := findFirst(text, `await\(async e=>e\+e\)\("([^"]+)"\)`)
	token := findFirst(text, `name="token" value="([^"]+)"`)
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
		"token":        {token},
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

func checkLoginResponseBody(ctx context.Context, body []byte) error {
	if log := zerolog.Ctx(ctx); log != nil {
		preview := strings.TrimSpace(string(body))
		if len(preview) > 300 {
			preview = preview[:300]
		}
		log.Debug().
			Int("body_len", len(body)).
			Str("body_preview", preview).
			Msg("Reddit password step returned 200")
	}
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
		msg := strings.TrimSpace(string(body))
		if len(msg) > 500 {
			msg = msg[:500]
		}
		return fmt.Errorf("reddit login: password step rejected: %s", msg)
	}
	return nil
}

func redditStatusError(resp *http.Response, body []byte) error {
	msg := strings.TrimSpace(string(body))
	if len(msg) > 500 {
		msg = msg[:500]
	}
	if msg == "" {
		msg = resp.Status
	}
	contentType := resp.Header.Get("Content-Type")
	location := resp.Header.Get("Location")
	extra := ""
	for _, h := range []string{"Cf-Ray", "Cf-Mitigated", "X-Ratelimit-Remaining"} {
		if v := resp.Header.Get(h); v != "" {
			extra += fmt.Sprintf(" %s=%q", strings.ToLower(h), v)
		}
	}
	return fmt.Errorf("reddit login: %s %s failed: status=%d content-type=%q location=%q%s body=%s",
		resp.Request.Method, resp.Request.URL.String(), resp.StatusCode, contentType, location, extra, msg)
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

func validCookieValue(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c < 0x21 || c == '"' || c == ';' || c == '\\' || c == 0x7f {
			return false
		}
	}
	return true
}
