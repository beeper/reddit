package redditchat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
)

var redditorIDPattern = regexp.MustCompile(`^t2_[a-z0-9]+$`)

// SetUserBlocked updates the account-wide Reddit block list, not room membership.
// The integrated chat UI uses the Reddit session cookie and CSRF token here,
// rather than the Matrix chat access token.
func (s *RedditSession) SetUserBlocked(ctx context.Context, redditorID string, blocked bool) error {
	if !redditorIDPattern.MatchString(redditorID) {
		return errors.New("reddit blocking: invalid redditor ID")
	}
	state := "NONE"
	if blocked {
		state = "BLOCKED"
	}
	var data struct {
		UpdateRedditorBlockState struct {
			OK bool `json:"ok"`
		} `json:"updateRedditorBlockState"`
	}
	err := s.chatGraphQL(ctx, "ChatUpdateRedditorBlockState", map[string]any{
		"input": map[string]string{"redditorId": redditorID, "blockState": state},
	}, &data)
	if err != nil {
		return err
	}
	if !data.UpdateRedditorBlockState.OK {
		return errors.New("reddit blocking: Reddit rejected the block list update")
	}
	return nil
}

// GetBlockedUsers returns native t2_ IDs. An absent or malformed list is an
// error: treating it as empty would incorrectly clear existing Beeper blocks.
func (s *RedditSession) GetBlockedUsers(ctx context.Context) ([]string, error) {
	var data struct {
		Identity struct {
			BlockedRedditorsInfo struct {
				Edges []struct {
					Node struct {
						ID string `json:"id"`
					} `json:"node"`
				} `json:"edges"`
			} `json:"blockedRedditorsInfo"`
		} `json:"identity"`
	}
	if err := s.chatGraphQL(ctx, "ChatBlockedRedditors", map[string]any{}, &data); err != nil {
		return nil, err
	}
	edges := data.Identity.BlockedRedditorsInfo.Edges
	if edges == nil {
		return nil, errors.New("reddit blocking: response did not include the blocked user list")
	}
	users := make([]string, 0, len(edges))
	for _, edge := range edges {
		if !redditorIDPattern.MatchString(edge.Node.ID) {
			return nil, errors.New("reddit blocking: invalid redditor ID in block list")
		}
		users = append(users, edge.Node.ID)
	}
	return users, nil
}

func (s *RedditSession) chatGraphQL(ctx context.Context, operation string, variables any, out any) error {
	if s == nil || s.csrfToken() == "" {
		return ErrSessionUnavailable
	}
	body, err := json.Marshal(struct {
		Operation string `json:"operation"`
		Variables any    `json:"variables"`
		CSRFToken string `json:"csrf_token"`
	}{operation, variables, s.csrfToken()})
	if err != nil {
		return err
	}
	req, err := s.newRequest(ctx, http.MethodPost, "/svc/shreddit/graphql", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", s.BaseURL)
	req.Header.Set("Referer", s.BaseURL+"/chat/")
	resp, body, err := s.do(req)
	if err != nil {
		return fmt.Errorf("reddit %s: %w", operation, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("reddit %s: HTTP %d", operation, resp.StatusCode)
	}
	var result struct {
		Data   json.RawMessage   `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	if err = json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("reddit %s: invalid JSON response", operation)
	}
	// GraphQL failures can arrive with HTTP 200, and even with partial data.
	// Do not expose arbitrary server error bodies in logs or Matrix notices.
	if len(result.Errors) > 0 {
		return fmt.Errorf("reddit %s: Reddit rejected the request", operation)
	}
	if len(result.Data) == 0 || bytes.Equal(result.Data, []byte("null")) {
		return fmt.Errorf("reddit %s: response did not include data", operation)
	}
	if err = json.Unmarshal(result.Data, out); err != nil {
		return fmt.Errorf("reddit %s: invalid response data", operation)
	}
	return nil
}
