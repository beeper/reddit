package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/id"

	"github.com/beeper/reddit/pkg/redditchat"
)

const (
	FlowIDPassword = "fi.mau.reddit.password"

	StepIDEnterCredentials = "fi.mau.reddit.enter_credentials"
	StepIDCaptchaPassword  = "fi.mau.reddit.captcha_password"
	StepIDEnterOTP         = "fi.mau.reddit.enter_otp"
	StepIDCaptchaOTP       = "fi.mau.reddit.captcha_otp"
	StepIDComplete         = "fi.mau.reddit.complete"

	FieldUsername       = "username"
	FieldPassword       = "password"
	FieldOTPCode        = "otp_code"
	FieldRecaptchaToken = "recaptcha_token"
)

func (rc *RedditConnector) GetLoginFlows() []bridgev2.LoginFlow {
	return []bridgev2.LoginFlow{
		{
			Name:        "Username & password",
			Description: "Log in with your Reddit account. CAPTCHA solving uses an embedded webview.",
			ID:          FlowIDPassword,
		},
	}
}

func (rc *RedditConnector) CreateLogin(ctx context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	if flowID != FlowIDPassword {
		return nil, fmt.Errorf("unknown login flow ID: %s", flowID)
	}
	return &PasswordLogin{main: rc, user: user}, nil
}

// PasswordLogin drives the multi-step Reddit login flow. Reddit needs:
//
//  1. username + password (entered in the bridge UI)
//  2. a reCAPTCHA Enterprise token for the password POST (solved in a webview)
//  3. optionally an OTP code (entered in the bridge UI)
//  4. optionally a second reCAPTCHA token for the OTP POST (solved in a webview)
//
// The redditchat library's LoginReddit takes a CaptchaTokenProvider callback
// that blocks until a token is returned. We run LoginReddit on a goroutine and
// pump CaptchaRequest values out / token strings in via channels, advancing the
// bridgev2 LoginStep state machine between each request.
type PasswordLogin struct {
	main *RedditConnector
	user *bridgev2.User

	username string
	password string

	// loginDone signals the LoginReddit goroutine has finished. Its result is
	// stored in loginResult / loginErr.
	loginDone   chan struct{}
	loginResult *redditchat.RedditLoginResult
	loginErr    error

	// captchaReq carries CaptchaRequest values from the library goroutine to
	// the SubmitUserInput / SubmitCookies handler. captchaResp returns the
	// solved token back to the library goroutine.
	captchaReq  chan redditchat.CaptchaRequest
	captchaResp chan captchaResponse

	cancelOnce sync.Once
	cancelFn   func()
}

type captchaResponse struct {
	token string
	err   error
}

var (
	_ bridgev2.LoginProcess          = (*PasswordLogin)(nil)
	_ bridgev2.LoginProcessUserInput = (*PasswordLogin)(nil)
	_ bridgev2.LoginProcessCookies   = (*PasswordLogin)(nil)
)

func (p *PasswordLogin) Cancel() {
	p.cancelOnce.Do(func() {
		if p.cancelFn != nil {
			p.cancelFn()
		}
		// Unblock anyone waiting on a response so the goroutine can exit.
		if p.captchaResp != nil {
			select {
			case p.captchaResp <- captchaResponse{err: errors.New("login cancelled")}:
			default:
			}
		}
	})
}

func (p *PasswordLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       StepIDEnterCredentials,
		Instructions: "Enter your Reddit username and password.",
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{
				{
					Type: bridgev2.LoginInputFieldTypeUsername,
					ID:   FieldUsername,
					Name: "Username",
				},
				{
					Type: bridgev2.LoginInputFieldTypePassword,
					ID:   FieldPassword,
					Name: "Password",
				},
			},
		},
	}, nil
}

// SubmitUserInput handles both the initial credentials step and the OTP step.
// The state machine distinguishes them by inspecting which channels are live.
func (p *PasswordLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	// First call: collect credentials and kick off the login goroutine.
	if p.loginDone == nil {
		username := strings.TrimSpace(input[FieldUsername])
		password := input[FieldPassword]
		if username == "" || password == "" {
			return nil, errors.New("username and password are required")
		}
		p.username = username
		p.password = password
		p.captchaReq = make(chan redditchat.CaptchaRequest, 1)
		p.captchaResp = make(chan captchaResponse, 1)
		p.loginDone = make(chan struct{})

		loginCtx, cancel := context.WithCancel(context.Background())
		p.cancelFn = cancel
		go p.runLogin(loginCtx)

		return p.awaitNextStep(ctx)
	}

	// Second call: OTP code from the user.
	otp := strings.TrimSpace(input[FieldOTPCode])
	if otp == "" {
		return nil, errors.New("OTP code is required")
	}
	// The redditchat library reads the OTP from RedditLoginOptions.TOTPCode,
	// not via a callback. So we restart the login flow with the OTP populated;
	// this re-runs the password POST (with a fresh CAPTCHA) and then the OTP
	// POST (with a second CAPTCHA). That's the shape Reddit expects anyway.
	return p.restartWithOTP(ctx, otp)
}

func (p *PasswordLogin) SubmitCookies(ctx context.Context, cookies map[string]string) (*bridgev2.LoginStep, error) {
	token := strings.TrimSpace(cookies[FieldRecaptchaToken])
	if token == "" {
		return nil, errors.New("missing reCAPTCHA token")
	}
	// Send the token to the waiting login goroutine.
	select {
	case p.captchaResp <- captchaResponse{token: token}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return p.awaitNextStep(ctx)
}

// awaitNextStep blocks until the login goroutine either:
//   - asks for another CAPTCHA token (return a Cookies step), or
//   - asks for an OTP code (return a UserInput step), or
//   - finishes (return a Complete step or error).
func (p *PasswordLogin) awaitNextStep(ctx context.Context) (*bridgev2.LoginStep, error) {
	select {
	case req := <-p.captchaReq:
		return p.buildCaptchaStep(req), nil
	case <-p.loginDone:
		if p.loginErr != nil {
			if errors.Is(p.loginErr, errLoginNeedsOTP) {
				return p.buildOTPStep(), nil
			}
			return nil, p.loginErr
		}
		return p.completeStep(ctx)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *PasswordLogin) buildCaptchaStep(req redditchat.CaptchaRequest) *bridgev2.LoginStep {
	stepID := StepIDCaptchaPassword
	if req.Step == redditchat.CaptchaStepOTP {
		stepID = StepIDCaptchaOTP
	}
	// Wrap the redditchat-provided JS so the Promise it returns resolves to
	// {recaptcha_token: "..."} — the shape expected by LoginCookiesParams.
	js := fmt.Sprintf(`(async () => { const token = await %s; return { %q: token }; })()`,
		req.EnterpriseExecuteJavaScript(), FieldRecaptchaToken)
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeCookies,
		StepID:       stepID,
		Instructions: "Solve the Reddit CAPTCHA in the webview.",
		CookiesParams: &bridgev2.LoginCookiesParams{
			URL:       req.PageURL,
			UserAgent: redditchat.DefaultRedditUserAgent,
			Fields: []bridgev2.LoginCookieField{
				{
					ID:       FieldRecaptchaToken,
					Required: true,
					Sources: []bridgev2.LoginCookieFieldSource{
						{Type: bridgev2.LoginCookieTypeSpecial, Name: "recaptcha"},
					},
				},
			},
			ExtractJS: js,
		},
	}
}

func (p *PasswordLogin) buildOTPStep() *bridgev2.LoginStep {
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       StepIDEnterOTP,
		Instructions: "Enter the 6-digit code from your authenticator app.",
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{
				{
					Type:    bridgev2.LoginInputFieldType2FACode,
					ID:      FieldOTPCode,
					Name:    "Authenticator code",
					Pattern: `^[0-9]{6}$`,
				},
			},
		},
	}
}

// runLogin executes the redditchat login flow on a goroutine. The
// CaptchaTokenProvider blocks until SubmitCookies pumps a token in.
func (p *PasswordLogin) runLogin(ctx context.Context) {
	provider := func(ctx context.Context, req redditchat.CaptchaRequest) (string, error) {
		select {
		case p.captchaReq <- req:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		select {
		case resp := <-p.captchaResp:
			if resp.err != nil {
				return "", resp.err
			}
			return resp.token, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}

	opts := redditchat.RedditLoginOptions{
		Username:             p.username,
		Password:             p.password,
		CaptchaTokenProvider: provider,
	}
	result, err := redditchat.LoginReddit(ctx, opts)

	// Detect 2FA-required responses. Reddit returns 202 from the password POST
	// when 2FA is needed; redditchat.submitPassword then asks for a second
	// captcha token and tries to read the OTP. With TOTPCode unset, it fails
	// with "otp required". We surface that as errLoginNeedsOTP so the caller
	// restarts with the OTP filled in.
	if err != nil && strings.Contains(err.Error(), "otp required") {
		err = errLoginNeedsOTP
	}

	p.loginResult = result
	p.loginErr = err
	close(p.loginDone)
}

// restartWithOTP restarts the login flow with the OTP code populated. The
// previous goroutine has already exited (with errLoginNeedsOTP) so we can spin
// up a fresh one with the same username/password plus TOTPCode. This
// re-triggers the password CAPTCHA and then the OTP CAPTCHA, but that's the
// shape Reddit's flow expects anyway.
func (p *PasswordLogin) restartWithOTP(ctx context.Context, otp string) (*bridgev2.LoginStep, error) {
	p.captchaReq = make(chan redditchat.CaptchaRequest, 1)
	p.captchaResp = make(chan captchaResponse, 1)
	p.loginDone = make(chan struct{})

	loginCtx, cancel := context.WithCancel(context.Background())
	p.cancelFn = cancel

	go func() {
		provider := func(ctx context.Context, req redditchat.CaptchaRequest) (string, error) {
			select {
			case p.captchaReq <- req:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			select {
			case resp := <-p.captchaResp:
				if resp.err != nil {
					return "", resp.err
				}
				return resp.token, nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		opts := redditchat.RedditLoginOptions{
			Username:             p.username,
			Password:             p.password,
			TOTPCode:             otp,
			CaptchaTokenProvider: provider,
		}
		result, err := redditchat.LoginReddit(loginCtx, opts)
		p.loginResult = result
		p.loginErr = err
		close(p.loginDone)
	}()

	return p.awaitNextStep(ctx)
}

func (p *PasswordLogin) completeStep(ctx context.Context) (*bridgev2.LoginStep, error) {
	if p.loginResult == nil {
		return nil, errors.New("login finished without a result")
	}
	creds := p.loginResult.Credentials
	cookiesJSON, err := serializeCookies(p.loginResult.Session)
	if err != nil {
		return nil, fmt.Errorf("serialize cookies: %w", err)
	}
	meta := &UserLoginMetadata{
		Credentials:     creds,
		CookiesJSON:     cookiesJSON,
		ChatTokenExpiry: p.loginResult.Token.Expires,
		Username:        p.username,
	}
	loginID := makeUserLoginID(id.UserID(creds.UserID))
	remoteName := p.username
	ul, err := p.user.NewLogin(ctx, &database.UserLogin{
		ID:         loginID,
		Metadata:   meta,
		RemoteName: remoteName,
		RemoteProfile: status.RemoteProfile{
			Name: remoteName,
		},
	}, &bridgev2.NewLoginParams{DeleteOnConflict: true})
	if err != nil {
		return nil, fmt.Errorf("save user login: %w", err)
	}
	go ul.Client.Connect(ul.Log.WithContext(context.Background()))
	return &bridgev2.LoginStep{
		Type:           bridgev2.LoginStepTypeComplete,
		StepID:         StepIDComplete,
		Instructions:   fmt.Sprintf("Logged in as %s", remoteName),
		CompleteParams: &bridgev2.LoginCompleteParams{UserLoginID: ul.ID, UserLogin: ul},
	}, nil
}

// errLoginNeedsOTP is an internal sentinel returned by runLogin when the
// initial password POST succeeds with 202 (2FA required). The caller catches
// it and prompts the user for an OTP code via buildOTPStep.
var errLoginNeedsOTP = errors.New("reddit login: needs OTP")

func serializeCookies(session *redditchat.RedditSession) (string, error) {
	if session == nil || session.Client == nil || session.Client.Jar == nil {
		return "", nil
	}
	u, err := url.Parse(session.BaseURL)
	if err != nil {
		return "", err
	}
	cookies := session.Client.Jar.Cookies(u)
	if len(cookies) == 0 {
		return "", nil
	}
	httpCookies := make([]*http.Cookie, 0, len(cookies))
	for _, c := range cookies {
		httpCookies = append(httpCookies, &http.Cookie{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			Secure:   c.Secure,
			HttpOnly: c.HttpOnly,
		})
	}
	b, err := json.Marshal(httpCookies)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
