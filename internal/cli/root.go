package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"tokgo/internal/app"
	"tokgo/internal/logx"
)

// Options are the build-time facts the command tree needs.
type Options struct {
	Version    string
	UpdateRepo string
	// TUI is injected because internal/tui validates its form through this
	// package, so this package cannot import it back.
	TUI func() error
}

// runError marks a failure from a run that started. Anything else cobra hands
// back came from reading the command line, so it exits 2 with the usage text
// the way the flag package did.
type runError struct{ err error }

func (r runError) Error() string { return r.err.Error() }
func (r runError) Unwrap() error { return r.err }

// New builds the tokgo command tree.
func New(opts Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tokgo",
		Short: "Record TikTok live streams to mp4",
		Long: "Records TikTok live streams to mp4 in the manual, automatic or\n" +
			"followers mode of tiktok-live-recorder, plus an interactive tui.",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd: true,
		},
	}
	f := bind(cmd.Flags())
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		banner(opts.Version)
		parsed, err := f.parsed()
		if err != nil {
			return err
		}
		if err := run(c.Context(), parsed, opts); err != nil {
			return runError{err}
		}
		return nil
	}
	cmd.AddCommand(tuiCommand(opts))
	return cmd
}

// Execute runs the command tree and returns the process exit status. Every line
// of output is printed here so the command functions stay silent.
func Execute(args []string, opts Options) int {
	cmd := New(opts)
	cmd.SetArgs(normalize(args, cmd.Flags()))

	err := cmd.Execute()
	if err == nil {
		return 0
	}
	var failure runError
	if !errors.As(err, &failure) {
		fmt.Fprintf(os.Stderr, "Error: %v\n\n%s", err, cmd.UsageString())
		return 2
	}
	// A cancelled run and a run whose per-user failures were already logged are
	// both finished business: report the status, not the error.
	switch {
	case errors.Is(err, context.Canceled):
		return 0
	case errors.Is(err, app.ErrReported):
		return 1
	}
	logx.Errorf("%v", err)
	return 1
}

func tuiCommand(opts Options) *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Interactive form for the same run",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if opts.TUI == nil {
				return runError{errors.New("tui is not available in this build")}
			}
			if err := opts.TUI(); err != nil {
				return runError{err}
			}
			return nil
		},
	}
}
