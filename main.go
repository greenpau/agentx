package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/greenpau/agentx/pkg/app"
	"github.com/greenpau/agentx/pkg/cli"
	"github.com/greenpau/agentx/pkg/signals"
)

func main() {
	code := runProcess(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Exit)
	if code != 0 {
		os.Exit(code)
	}
}

func runProcess(args []string, stdin io.Reader, stdout, stderr io.Writer, forceExit func(int)) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shutdownState := signals.NewProcessShutdown(forceExit, signals.DefaultFailsafe)
	ctx = signals.WithProcessShutdown(ctx, shutdownState)
	interruptOwner := signals.InterruptOwnedByProcess
	if cli.HeadlessRequested(args, writerIsTerminal(stdout)) {
		interruptOwner = signals.InterruptOwnedByPrint
	}
	stopSignals, err := signals.StartProcessMonitor(cancel, shutdownState, interruptOwner)
	if err != nil {
		fmt.Fprintln(stderr, app.TerminalSafeText(err.Error()))
		return 1
	}
	defer func() { _ = stopSignals() }()

	err = app.Run(ctx, args, stdin, stdout, stderr)
	if stopErr := stopSignals(); stopErr != nil {
		fmt.Fprintln(stderr, app.TerminalSafeText(stopErr.Error()))
		return 1
	}
	if err != nil {
		fmt.Fprintln(stderr, app.TerminalSafeText(err.Error()))
		return shutdownState.ExitCode(app.ExitCode(err))
	}
	// A surface may finish graceful cleanup and return nil after its context is
	// cancelled. Preserve the initiating signal's process contract even then.
	return shutdownState.ExitCode(0)
}

func writerIsTerminal(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
