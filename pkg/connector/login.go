package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

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
	FieldClientVersion  = "client_version"
)

func (rc *RedditConnector) GetLoginFlows() []bridgev2.LoginFlow {
	return []bridgev2.LoginFlow{
		{
			Name:        "Username & password",
			Description: "Log in with your Reddit account. Verification runs in the background.",
			ID:          FlowIDPassword,
		},
	}
}

func (rc *RedditConnector) CreateLogin(ctx context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	if flowID != FlowIDPassword {
		return nil, bridgev2.ErrInvalidLoginFlowID
	}
	loginCtx, cancel := context.WithCancel(context.Background())
	return &PasswordLogin{user: user, ctx: loginCtx, cancel: cancel, step: StepIDEnterCredentials}, nil
}

// PasswordLogin maps Reddit's password, verification and OTP operations directly
// onto bridgev2 login steps. No native worker runs between steps.
type PasswordLogin struct {
	user                    *bridgev2.User
	ctx                     context.Context
	cancel                  context.CancelFunc
	step                    string
	session                 *redditchat.RedditSession
	username, password, otp string
}

var (
	_ bridgev2.LoginProcess          = (*PasswordLogin)(nil)
	_ bridgev2.LoginProcessUserInput = (*PasswordLogin)(nil)
	_ bridgev2.LoginProcessCookies   = (*PasswordLogin)(nil)
)

func (p *PasswordLogin) Cancel() { p.cancel() }

// Keep process cancellation effective even if it races with a provisioning
// worker starting a step. The supplied context also cancels that step's IO.
func (p *PasswordLogin) stepContext(ctx context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(p.ctx, cancel)
	if p.ctx.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}

func (p *PasswordLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
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

func (p *PasswordLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	ctx, done := p.stepContext(ctx)
	defer done()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch p.step {
	case StepIDEnterCredentials:
		p.username, p.password = strings.TrimSpace(input[FieldUsername]), input[FieldPassword]
		if p.username == "" || p.password == "" {
			return nil, ErrLoginMissingCredentials
		}
		var err error
		p.session, err = redditchat.NewRedditSession(nil, "", "")
		if err == nil {
			err = p.session.PrepareLogin(ctx, p.username)
		}
		if err != nil {
			return nil, wrapRedditLoginError(err)
		}
		return p.buildCaptchaStep(p.session.CaptchaRequest(redditchat.CaptchaStepPassword)), nil
	case StepIDEnterOTP:
		p.otp = strings.TrimSpace(input[FieldOTPCode])
		if len(p.otp) != 6 || strings.Trim(p.otp, "0123456789") != "" {
			return nil, ErrLoginMissingOTP
		}
		return p.buildCaptchaStep(p.session.CaptchaRequest(redditchat.CaptchaStepOTP)), nil
	default:
		return nil, errors.New("reddit login is not waiting for user input")
	}
}

func (p *PasswordLogin) SubmitCookies(ctx context.Context, cookies map[string]string) (*bridgev2.LoginStep, error) {
	ctx, done := p.stepContext(ctx)
	defer done()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.step != StepIDCaptchaPassword && p.step != StepIDCaptchaOTP {
		return nil, errors.New("reddit login is not waiting for verification")
	}
	captcha := redditchat.CaptchaResult{
		Token:         strings.TrimSpace(cookies[FieldRecaptchaToken]),
		ClientVersion: strings.TrimSpace(cookies[FieldClientVersion]),
	}
	if captcha.Token == "" {
		return nil, ErrLoginVerificationFailed
	}
	var err error
	if p.step == StepIDCaptchaPassword {
		var needsOTP bool
		needsOTP, err = p.session.SubmitPassword(ctx, p.username, p.password, captcha)
		if err == nil && needsOTP {
			return p.buildOTPStep(), nil
		}
	} else {
		err = p.session.SubmitOTP(ctx, p.username, p.password, p.otp, captcha)
	}
	if err != nil {
		return nil, wrapRedditLoginError(err)
	}
	return p.completeStep(ctx)
}

func (p *PasswordLogin) buildCaptchaStep(req redditchat.CaptchaRequest) *bridgev2.LoginStep {
	stepID := StepIDCaptchaPassword
	if req.Step == redditchat.CaptchaStepOTP {
		stepID = StepIDCaptchaOTP
	}
	p.step = stepID
	// Wrap the redditchat-provided JS so the Promise it returns resolves to
	// {recaptcha_token: "..."} — the shape expected by LoginCookiesParams.
	js := fmt.Sprintf(`(async () => { const token = await %s; return { %q: token }; })()`,
		req.EnterpriseExecuteJavaScript(), FieldRecaptchaToken)
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeCookies,
		StepID:       stepID,
		Instructions: "Verifying with Reddit…",
		CookiesParams: &bridgev2.LoginCookiesParams{
			URL:       req.PageURL,
			UserAgent: redditchat.DefaultRedditUserAgent,
			Hidden:    true,
			Fields: []bridgev2.LoginCookieField{
				{
					ID:       FieldRecaptchaToken,
					Required: true,
					Sources: []bridgev2.LoginCookieFieldSource{
						{Type: bridgev2.LoginCookieTypeSpecial, Name: "recaptcha"},
					},
				},
				{
					// Sniff the real X-Reddit-Client-Version off the shreddit
					// page's own /svc/ requests. Optional: when the client can't
					// capture it, redditchat uses its default client version.
					ID:       FieldClientVersion,
					Required: false,
					Sources: []bridgev2.LoginCookieFieldSource{
						{
							Type:            bridgev2.LoginCookieTypeRequestHeader,
							Name:            "X-Reddit-Client-Version",
							RequestURLRegex: `https://www\.reddit\.com/svc/`,
						},
					},
				},
			},
			ExtractJS: js,
		},
	}
}

func (p *PasswordLogin) buildOTPStep() *bridgev2.LoginStep {
	p.step = StepIDEnterOTP
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

func (p *PasswordLogin) completeStep(ctx context.Context) (*bridgev2.LoginStep, error) {
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	defer func() { p.password, p.otp = "", "" }()
	token, creds, err := redditchat.CredentialsFromRedditSession(ctx, p.session)
	if err != nil {
		return nil, wrapRedditLoginError(err)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	cookiesJSON, err := serializeCookies(p.session)
	if err != nil {
		return nil, fmt.Errorf("serialize cookies: %w", err)
	}
	meta := &UserLoginMetadata{
		Credentials:     creds,
		CookiesJSON:     cookiesJSON,
		ChatTokenExpiry: token.Expires,
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
	p.step = StepIDComplete
	go ul.Client.Connect(ul.Log.WithContext(context.Background()))
	return &bridgev2.LoginStep{
		Type:           bridgev2.LoginStepTypeComplete,
		StepID:         StepIDComplete,
		Instructions:   fmt.Sprintf("Logged in as %s", remoteName),
		CompleteParams: &bridgev2.LoginCompleteParams{UserLoginID: ul.ID, UserLogin: ul},
	}, nil
}

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
