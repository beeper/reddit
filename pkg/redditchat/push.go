package redditchat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const FCMSenderID = "933360113277"

var ErrPushAuthentication = errors.New("reddit push: authentication required")

// Reddit Android 2026.41.0 uses RegisterMobilePushToken, not Matrix pushers.
// This registration has no per-token notification category filter.
func (c *Client) RegisterFCMPush(ctx context.Context, pushToken, deviceID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil || c.Matrix == nil || strings.TrimSpace(c.Matrix.AccessToken) == "" {
		return ErrPushAuthentication
	}
	if strings.TrimSpace(pushToken) == "" || strings.TrimSpace(deviceID) == "" {
		return errors.New("reddit push: FCM token and device ID are required")
	}
	body, err := json.Marshal(map[string]any{
		"operationName": "RegisterMobilePushToken",
		"variables": map[string]any{
			"authTokens":   []string{c.Matrix.AccessToken},
			"pushToken":    pushToken,
			"deviceId":     deviceID,
			"timezoneName": "UTC",
			"timestamp":    time.Now().UTC().Format(time.RFC3339Nano),
			"language":     "en",
		},
		"extensions": map[string]any{
			"persistedQuery": map[string]any{
				"version":    1,
				"sha256Hash": "35313b81bfbb621c426b2f2ec276db3db476a5d8362a1ddbeec982a01dc74977",
			},
		},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://gql-fed.reddit.com/", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Matrix.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Reddit/Version 2026.41.0/Build 2641021/Android 15")
	req.Header.Set("X-Reddit-Device-Id", deviceID)
	// The body contains both credentials and a push token. Avoid the Matrix
	// request logger and redirects, which could forward the body to another host.
	httpClient := *c.Matrix.Client
	httpClient.Jar = nil
	if httpClient.Timeout == 0 {
		httpClient.Timeout = 30 * time.Second
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("reddit push: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrPushAuthentication
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("reddit push: registration returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		Data struct {
			RegisterMobilePushToken struct {
				OK bool `json:"ok"`
			} `json:"registerMobilePushToken"`
		} `json:"data"`
		Errors []struct {
			Extensions struct {
				Code string `json:"code"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&result); err != nil {
		return errors.New("reddit push: invalid registration response")
	}
	for _, gqlErr := range result.Errors {
		if gqlErr.Extensions.Code == "NOT_AUTHENTICATED" {
			return ErrPushAuthentication
		}
	}
	if len(result.Errors) > 0 || !result.Data.RegisterMobilePushToken.OK {
		return errors.New("reddit push: Reddit rejected registration")
	}
	return nil
}
