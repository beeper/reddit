package redditchat

import "context"

// GetRedditorIcons returns avatar URLs keyed by t2_ ID. Reddit's chat Matrix
// server has no avatars; the integrated chat UI reads them from here.
func (s *RedditSession) GetRedditorIcons(ctx context.Context, redditorIDs []string) (map[string]string, error) {
	var data struct {
		RedditorsInfoByIDs []struct {
			ID   string `json:"id"`
			Icon *struct {
				URL string `json:"url"`
			} `json:"icon"`
		} `json:"redditorsInfoByIds"`
	}
	err := s.chatGraphQL(ctx, "ChatRedditors", map[string]any{"ids": redditorIDs, "includeRedditorAttributes": true}, &data)
	if err != nil {
		return nil, err
	}
	icons := make(map[string]string, len(data.RedditorsInfoByIDs))
	for _, redditor := range data.RedditorsInfoByIDs {
		if redditor.Icon != nil && redditor.Icon.URL != "" {
			icons[redditor.ID] = redditor.Icon.URL
		}
	}
	return icons, nil
}
