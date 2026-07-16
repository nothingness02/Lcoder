package main

import (
	"context"
	"fmt"
	"os"

	bridge "github.com/lcoder/lcoder/cmd/lcoder-desktop/internal/bridge"
	"github.com/lcoder/lcoder/pkg/checkpoint"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/desktop"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx          context.Context
	runtime      *desktop.Runtime
	AgentService *AgentService
	bridge       *bridge.EventBridge
	persister    *desktop.SessionPersister
}

func NewApp() *App {
	return &App{AgentService: &AgentService{}}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	cwd, err := os.Getwd()
	if err != nil {
		a.fatal("get cwd", err)
		return
	}

	cfg, err := config.Load()
	if err != nil {
		a.fatal("load config", err)
		return
	}
	if err := cfg.Validate(); err != nil {
		a.fatal("validate config", err)
		return
	}

	setup, err := prepareAgent(cfg, cwd, "code", "", false)
	if err != nil {
		a.fatal("prepare agent", err)
		return
	}

	agentSetup := &desktop.AgentSetup{
		Agent:           setup.ag,
		Session:         setup.sess,
		SessionStore:    setup.store,
		Bus:             setup.bus,
		Config:          setup.cfg.Config,
		CWD:             setup.cwd,
		LLMClient:       setup.llmClient,
		CheckpointStore: setup.checkpointStore,
		ModeManager:     setup.cfg.modeManager,
		MCPRegistry:     setup.mcpRegistry,
		Cleanup:         setup.cleanup,
	}

	a.runtime = desktop.NewRuntime(agentSetup)
	a.runtime.Agent.SetUserConfirm(a.runtime.Permissions)
	a.AgentService.runtime = a.runtime

	a.persister = desktop.NewSessionPersister(a.runtime, a.runtime.Bus)
	a.bridge = bridge.NewEventBridge(ctx, a.runtime.Bus)
	a.bridge.Start()

	runtime.EventsEmit(ctx, "app:ready", map[string]any{
		"config":   a.AgentService.GetConfig(),
		"messages": a.AgentService.GetMessages(),
	})
}

func (a *App) shutdown(ctx context.Context) {
	if a.runtime != nil {
		a.runtime.Agent.Abort()
		_ = a.writeCheckpoint(checkpoint.ReasonManual)
		if a.persister != nil {
			a.persister.Close()
		}
		if a.bridge != nil {
			a.bridge.Stop()
		}
		if a.runtime.Cleanup != nil {
			a.runtime.Cleanup()
		}
	}
}

func (a *App) beforeClose(ctx context.Context) bool {
	_ = a.writeCheckpoint(checkpoint.ReasonCrash)
	return false
}

func (a *App) writeCheckpoint(reason string) error {
	if a.runtime == nil || a.runtime.Agent == nil || a.runtime.CheckpointStore == nil {
		return nil
	}
	cp, err := a.runtime.Agent.CheckpointWithReason(reason)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to capture %s checkpoint: %v\n", reason, err)
		return err
	}
	if err := a.runtime.CheckpointStore.Save(a.runtime.Agent.SessionID(), cp); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to save %s checkpoint: %v\n", reason, err)
		return err
	}
	return nil
}

func (a *App) fatal(what string, err error) {
	msg := fmt.Sprintf("%s: %v", what, err)
	fmt.Fprintln(os.Stderr, msg)
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "app:fatal", map[string]string{"message": msg})
	}
}
