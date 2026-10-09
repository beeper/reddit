package redditchat

import (
	_ "embed"
	"encoding/json"
)

// Native Reddit Chat's reaction picker, observed 2026-10-06. Reactions use
// static i.redd.it asset basenames, not Unicode. Keep this separate from the
// connector's optional Unicode aliases and reject unrecognized outbound keys.
//
//go:embed reactions.json
var reactionCatalogJSON []byte

type ReactionAsset struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

func ReactionAssets() []ReactionAsset {
	var assets []ReactionAsset
	if err := json.Unmarshal(reactionCatalogJSON, &assets); err != nil {
		panic(err) // Invalid embedded build input, never provider data.
	}
	return assets
}

func ReactionAssetByKey(key string) (ReactionAsset, bool) {
	for _, asset := range ReactionAssets() {
		if asset.Key == key {
			return asset, true
		}
	}
	return ReactionAsset{}, false
}
