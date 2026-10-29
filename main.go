// Command tokgo records TikTok live streams to mp4.
package main

import (
	"os"

	"tokgo/internal/cli"
	"tokgo/internal/logx"
	"tokgo/internal/tui"
)

const version = "0.2.0"

// Builds that are distributed through GitHub releases can set this; a plain
// source build has nobody to ask, so the update check stays offline.
const updateRepo = ""

func main() {
	logx.Init(logx.DefaultPath)
	code := cli.Execute(os.Args[1:], cli.Options{
		Version:    version,
		UpdateRepo: updateRepo,
		TUI:        func() error { return tui.Run(version) },
	})
	logx.Close()
	os.Exit(code)
}
