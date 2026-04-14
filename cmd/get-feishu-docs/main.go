package main

import (
	"context"
	"fmt"
	"os"

	"github.com/wangnov/get-feishu-docs/internal/browser"
	"github.com/wangnov/get-feishu-docs/internal/capture"
	"github.com/wangnov/get-feishu-docs/internal/cli"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	root := cli.NewRootCommand(cli.Config{
		OutWriter: os.Stdout,
		ErrWriter: os.Stderr,
		InReader:  os.Stdin,
		Handlers: cli.Handlers{
			Capture: func(req cli.CaptureRequest, emit func(cli.ProgressEvent)) (cli.Result, error) {
				return capture.Run(context.Background(), req, emit)
			},
			Doctor:         browser.Doctor,
			BrowserStatus:  browser.BrowserStatus,
			BrowserInstall: browser.BrowserInstall,
		},
	})
	root.Version = buildVersion()

	err := root.Execute()
	if err != nil {
		os.Exit(int(cli.ExitForError(err)))
	}
}

func buildVersion() string {
	return fmt.Sprintf("%s\ncommit=%s\ndate=%s", version, commit, date)
}
