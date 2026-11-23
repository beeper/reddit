package connector

import (
	"go.mau.fi/util/l10n"
)

var L = l10n.NewNamespace("reddit")

var (
	msgFlowPasswordName        = L.Define("login.flow.password.name", "Username & password")
	msgFlowPasswordDescription = L.Define("login.flow.password.description", "Log in with your Reddit account. Verification runs in the background.")

	msgFieldUsernameName = L.Define("login.field.username.name", "Username")
	msgFieldPasswordName = L.Define("login.field.password.name", "Password")
	msgFieldOTPName      = L.Define("login.field.otp_code.name", "Authenticator code")

	msgStepCredentialsInstructions = L.Define("login.step.credentials.instructions", "Enter your Reddit username and password.")
	msgStepCaptchaInstructions     = L.Define("login.step.captcha.instructions", "Verifying with Reddit…")
	msgStepOTPInstructions         = L.Define("login.step.otp.instructions", "Enter the 6-digit code from your authenticator app.")
	msgStepCompleteInstructions    = L.Define(
		"login.step.complete.instructions", "Logged in as {username}",
		l10n.WithComment("username is the Reddit username of the logged-in account"),
	)
)
