package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/greenpau/versioned"

	agentapp "github.com/greenpau/agentx/pkg/app"
	"github.com/greenpau/agentx/pkg/cli"
	"github.com/greenpau/agentx/pkg/signals"
)

var (
	app        *versioned.PackageManager
	appVersion string
	gitBranch  string
	gitCommit  string
	buildUser  string
	buildDate  string
)

func init() {
	app = versioned.NewPackageManager("agentx")
	app.Description = "Terminal-first agentic software-engineering client"
	app.Documentation = "https://github.com/greenpau/agentx/"
	app.SetVersion(appVersion, "1.0.7")
	app.SetGitBranch(gitBranch, "")
	app.SetGitCommit(gitCommit, "1.0.7")
	app.SetBuildUser(buildUser, "")
	app.SetBuildDate(buildDate, "")
	agentapp.ConfigureBuildIdentity(app.Version, app.Banner())
}

func main() {
	code := runProcess(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Exit)
	if code != 0 {
		os.Exit(code)
	}
}

func runProcess(args []string, stdin io.Reader, stdout, stderr io.Writer, forceExit func(int)) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var err error
	ctx, err = agentapp.PrepareApplicationHome(ctx)
	if err != nil {
		fmt.Fprintln(stderr, agentapp.TerminalSafeText(err.Error()))
		return 1
	}
	if err = agentapp.RequireApplicationAuth(ctx); err != nil {
		fmt.Fprintln(stderr, agentapp.TerminalSafeText(err.Error()))
		return 1
	}
	// Registry discovery is a finite, provider-neutral configuration read. Keep
	// it ahead of process signal ownership so editor probes cannot construct
	// session lifecycle machinery merely to inspect static endpoint metadata.
	if options, parseErr := cli.Parse(args); parseErr == nil && options.ProviderDiscoveryRequested() {
		err = agentapp.Run(ctx, args, stdin, stdout, stderr)
		if err != nil {
			fmt.Fprintln(stderr, agentapp.TerminalSafeText(err.Error()))
		}
		return agentapp.ExitCode(err)
	}
	shutdownState := signals.NewProcessShutdown(forceExit, signals.DefaultFailsafe)
	ctx = signals.WithProcessShutdown(ctx, shutdownState)
	interruptOwner := signals.InterruptOwnedByProcess
	if cli.HeadlessRequested(args, writerIsTerminal(stdout)) {
		interruptOwner = signals.InterruptOwnedByPrint
	}
	stopSignals, err := signals.StartProcessMonitor(cancel, shutdownState, interruptOwner)
	if err != nil {
		fmt.Fprintln(stderr, agentapp.TerminalSafeText(err.Error()))
		return 1
	}
	defer func() { _ = stopSignals() }()

	err = agentapp.Run(ctx, args, stdin, stdout, stderr)
	if stopErr := stopSignals(); stopErr != nil {
		fmt.Fprintln(stderr, agentapp.TerminalSafeText(stopErr.Error()))
		return 1
	}
	if err != nil {
		fmt.Fprintln(stderr, agentapp.TerminalSafeText(err.Error()))
		return shutdownState.ExitCode(agentapp.ExitCode(err))
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
