package main

import (
	"maunium.net/go/mautrix/bridgev2/matrix/mxmain"

	"github.com/beeper/reddit/pkg/connector"
)

var (
	Tag       = "unknown"
	Commit    = "unknown"
	BuildTime = "unknown"
)

var m = mxmain.BridgeMain{
	Name:        "reddit",
	URL:         "https://github.com/beeper/reddit",
	Description: "A Matrix-Reddit chat puppeting bridge.",
	Version:     "0.1.0",
	Connector:   &connector.RedditConnector{},
}

func main() {
	m.InitVersion(Tag, Commit, BuildTime)
	m.Run()
}
