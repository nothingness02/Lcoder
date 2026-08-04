package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/lcoder/lcoder/pkg/agentapi"
	"github.com/lcoder/lcoder/pkg/checkpoint"
	"github.com/lcoder/lcoder/pkg/host"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/rpcserver"
	"github.com/lcoder/lcoder/pkg/session"
)

// rpcCmd serves the JSONL RPC protocol (docs/rpc-protocol.md) over
// stdin/stdout so out-of-process UIs in any language can drive the agent.
// stdout is reserved for protocol frames; all diagnostics stay on stderr.
func rpcCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rpc",
		Short: "Serve JSONL RPC over stdin/stdout (headless UI protocol)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateRPCModeFlags(goalText, promptText, jsonMode); err != nil {
				return err
			}
			promptText = ""
			return runRPC(cmd)
		},
	}
}

// validateRPCModeFlags rejects the root command's headless run-mode flags
// when combined with rpc: prompts and goals arrive over the protocol, so a
// flag would otherwise be silently ignored. (They are root-local flags
// today, so cobra rejects them before this runs — this stays as the
// explicit guard in case they ever become persistent.)
func validateRPCModeFlags(goal, prompt string, json bool) error {
	switch {
	case goal != "":
		return fmt.Errorf("--goal cannot be used with 'lcoder rpc'; drive goals with the goal_start command")
	case prompt != "":
		return fmt.Errorf("--prompt cannot be used with 'lcoder rpc'; send prompt commands over stdin")
	case json:
		return fmt.Errorf("--json cannot be used with 'lcoder rpc'; the rpc protocol is already JSONL")
	}
	return nil
}

func runRPC(cmd *cobra.Command) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	setup, err := prepareAgent(cfg, cwd)
	if err != nil {
		return err
	}
	defer setup.cleanup()

	ctx, stop := signal.NotifyContext(cmd.Context(), shutdownSignals...)
	defer stop()

	// Assemble the protocol core exactly like runTUI: session mirror, goal
	// driver, and the session-swap notifications live in the host.
	core := host.NewCore(setup.ag, setup.bus, setup.store, setup.sess, setup.cwd,
		func(s *session.Session) {
			setup.activeSession.Set(s)
			if setup.subagentHost != nil {
				setup.subagentHost.SetParentSession(s.ID)
			}
		})
	defer core.Close()

	var caps []string
	if meta, ok := setup.cfg.ModelMeta(); ok {
		caps = meta.Capabilities
	}
	srv := rpcserver.New(core, setup.bus, rpcserver.Options{
		Model:        models.ModelRef{Provider: setup.cfg.Provider, ID: setup.cfg.Model},
		Capabilities: caps,
		// set_model's budget derivation mirrors the TUI provider panel:
		// catalog window/maxOutput + config.ResolveContextBudget, with the
		// configured static-block ratio carried over.
		ResolveBudget: func(ctx context.Context, ref models.ModelRef) (agentapi.TokenBudget, error) {
			window, _ := setup.llmClient.ModelWindow(ctx, ref.Provider, ref.ID)
			maxOutput, _ := setup.llmClient.ModelMaxOutput(ctx, ref.Provider, ref.ID)
			maxInput, _ := setup.llmClient.ModelMaxInput(ctx, ref.Provider, ref.ID)
			budget, source := setup.cfg.ResolveContextBudget(window, maxOutput)
			budget.ClampToMaxInput(maxInput, source)
			return agentapi.TokenBudget{
				MaxTotal:      budget.MaxTotal,
				TargetTotal:   budget.TargetTotal,
				ReserveOutput: budget.ReserveOutput,
				MaxOutput:     budget.MaxOutput,
				DropThreshold: budget.DropThreshold,
				StaticRatio:   setup.cfg.Context.StaticRatio,
			}, nil
		},
	})
	// Subagent tool calls are approved over the same client dialog; wiring it
	// here keeps a mode switch inside the core from disturbing it.
	if setup.subagentHost != nil {
		setup.subagentHost.SetUserConfirm(srv.Confirmation())
	}

	runErr := srv.Serve(ctx, os.Stdin, os.Stdout)
	if ctx.Err() != nil {
		writeCrashCheckpoint(setup)
	} else if runErr == nil {
		writeBestEffortCheckpoint(setup, checkpoint.ReasonManual)
	}
	return runErr
}
