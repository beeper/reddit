package redditchat

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGenerateTOTP(t *testing.T) {
	code, err := GenerateTOTP("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", time.Unix(59, 0))
	if err != nil {
		t.Fatal(err)
	}
	if code != "287082" {
		t.Fatalf("unexpected code: got %s want 287082", code)
	}
}

func TestCredentialsFromChatToken(t *testing.T) {
	token := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"lid":"t2_example"}`)) + ".sig"
	creds, err := credentialsFromChatToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if creds.Homeserver != DefaultHomeserver {
		t.Fatalf("homeserver = %q", creds.Homeserver)
	}
	if creds.UserID != "@t2_example:reddit.com" {
		t.Fatalf("user ID = %q", creds.UserID)
	}
	if creds.AccessToken != token {
		t.Fatal("access token was not preserved")
	}
}

func TestStaticCaptchaTokenProvider(t *testing.T) {
	provider := StaticCaptchaTokenProvider(" first ", "second")
	token, err := provider(t.Context(), CaptchaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if token != "first" {
		t.Fatalf("first token = %q", token)
	}
	token, err = provider(t.Context(), CaptchaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if token != "second" {
		t.Fatalf("second token = %q", token)
	}
	_, err = provider(t.Context(), CaptchaRequest{SiteKey: "site", Action: "act", PageURL: "page", Step: CaptchaStepOTP})
	if !errors.Is(err, ErrCaptchaRequired) {
		t.Fatalf("expected ErrCaptchaRequired, got %v", err)
	}
	var captchaErr *CaptchaRequiredError
	if !errors.As(err, &captchaErr) {
		t.Fatalf("expected CaptchaRequiredError, got %T", err)
	}
	if captchaErr.Request.SiteKey != "site" || captchaErr.Request.Action != "act" || captchaErr.Request.PageURL != "page" || captchaErr.Request.Step != CaptchaStepOTP {
		t.Fatalf("request not preserved: %#v", captchaErr.Request)
	}
}

func TestCaptchaTokenRequiredError(t *testing.T) {
	_, err := RedditLoginOptions{}.captchaToken(t.Context(), DefaultRedditURL, CaptchaStepPassword)
	if !errors.Is(err, ErrCaptchaRequired) {
		t.Fatalf("expected ErrCaptchaRequired, got %v", err)
	}
	var captchaErr *CaptchaRequiredError
	if !errors.As(err, &captchaErr) {
		t.Fatalf("expected CaptchaRequiredError, got %T", err)
	}
	if captchaErr.Request.SiteKey != RedditLoginCaptchaSiteKey {
		t.Fatalf("site key = %q", captchaErr.Request.SiteKey)
	}
	if captchaErr.Request.Action != RedditLoginCaptchaAction {
		t.Fatalf("action = %q", captchaErr.Request.Action)
	}
	if captchaErr.Request.PageURL != DefaultRedditURL+"/login/" {
		t.Fatalf("page url = %q", captchaErr.Request.PageURL)
	}
	if captchaErr.Request.Step != CaptchaStepPassword {
		t.Fatalf("step = %q", captchaErr.Request.Step)
	}
}

func TestCaptchaRequestHelpers(t *testing.T) {
	req := CaptchaRequest{SiteKey: "site key", Action: "act"}
	if got := req.EnterpriseScriptURL(); got != "https://www.google.com/recaptcha/enterprise.js?render=site+key" {
		t.Fatalf("script url = %q", got)
	}
	js := req.EnterpriseExecuteJavaScript()
	for _, want := range []string{"grecaptcha.enterprise.execute", `"site key"`, `"act"`} {
		if !strings.Contains(js, want) {
			t.Fatalf("execute js missing %q: %s", want, js)
		}
	}
}
