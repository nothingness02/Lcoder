package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/lcoder/lcoder/internal/paths"
	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/agent/hooks"
	"github.com/lcoder/lcoder/pkg/agenthost"
	"github.com/lcoder/lcoder/pkg/agentsetup"
	"github.com/lcoder/lcoder/pkg/checkpoint"
	"github.com/lcoder/lcoder/pkg/config"
	contextloader "github.com/lcoder/lcoder/pkg/context"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/extension/bridge"
	extruntime "github.com/lcoder/lcoder/pkg/extension/runtime"
	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/mcp"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/observability"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/session"
	"github.com/lcoder/lcoder/pkg/skills"
	"github.com/lcoder/lcoder/pkg/subagent"
	"github.com/lcoder/lcoder/pkg/tools"
	builtinTools "github.com/lcoder/lcoder/pkg/tools/builtin"
	"github.com/lcoder/lcoder/pkg/tui"
)

var (
	cfgFile                string
	modelID                string
	provider               string
	sessionID              string
	cont                   bool
	modeName               string
	promptText             string
	unsafeMode             bool
	jsonMode               bool
	trustProjectExtensions bool
)

func main() {
	root := &cobra.Command{
		Use:   "lcoder [prompt]",
		Short: "Lcoder — a minimal, extensible SWE agent",
		RunE:  runRoot,
	}

	// Persistent flags apply to the root command and subcommands that run the
	// agent (e.g. "lcoder tui"); --prompt and --json stay root-local because
	// they select the one-shot/JSON run modes.
	root.PersistentFlags().StringVar(&cfgFile, "config", "", "Path to config file")
	root.PersistentFlags().StringVar(&modelID, "model", "", "Model ID")
	root.PersistentFlags().StringVar(&provider, "provider", "", "Model provider")
	root.PersistentFlags().StringVar(&sessionID, "session", "", "Session ID to resume")
	root.PersistentFlags().BoolVarP(&cont, "continue", "c", false, "Continue most recent session")
	root.PersistentFlags().StringVar(&modeName, "mode", "", "Agent mode: plan, code, explore, review, test")
	root.Flags().StringVarP(&promptText, "prompt", "p", "", "Single prompt to run and exit")
	root.Flags().BoolVar(&jsonMode, "json", false, "Output events as JSONL instead of TUI/text")
	root.PersistentFlags().BoolVar(&trustProjectExtensions, "trust-project-extensions", false, "Load project-level extensions without prompting")
	root.PersistentFlags().BoolVar(&unsafeMode, "unsafe", false, "Bypass permission engine (ultra-destructive commands still require approval)")

	root.AddCommand(modelsCmd())
	root.AddCommand(skillsCmd())
	root.AddCommand(sessionsCmd())
	root.AddCommand(modesCmd())
	root.AddCommand(statsCmd())
	root.AddCommand(exportCmd())
	root.AddCommand(traceCmd())
	root.AddCommand(metricsCmd())
	root.AddCommand(tuiCmd())
	root.AddCommand(installCmd())
	root.AddCommand(uninstallCmd())
	root.AddCommand(listExtensionsCmd())
	root.AddCommand(updateCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func loadConfig() (config.Config, error) {
	cfg := config.DefaultConfig()
	if cfgFile != "" {
		data, err := os.ReadFile(cfgFile)
		if err != nil {
			return cfg, err
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return cfg, err
		}
		config.Finalize(&cfg)
	} else {
		loaded, err := config.Load()
		if err != nil {
			return cfg, err
		}
		cfg = loaded
	}
	if modelID != "" {
		cfg.Model = modelID
	}
	if provider != "" {
		cfg.Provider = provider
	}
	if unsafeMode {
		cfg.Permissions.UnsafeMode = true
	}
	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

type agentSetup struct {
	ag              *agent.Agent
	sess            *session.Session
	activeSession   *agentsetup.ActiveSession
	store           *session.Store
	bus             *events.Bus
	mcpRegistry     *mcp.Registry
	cfg             agentConfig
	cwd             string
	llmClient       *llm.Client
	checkpointStore checkpoint.Store
	obsWatcher      *observability.ConfigWatcher
	subagentHost    *agenthost.Host
	extHost         *extruntime.Host  // nil when no extensions loaded
	extBridge       *bridge.Bridge // nil when no extensions loaded
	cleanup         func()
}

type agentConfig struct {
	config.Config
	skillCatalog       *skills.Catalog
	modeManager        *agent.ModeManager
}

func prepareAgent(cfg config.Config, cwd string) (*agentSetup, error) {
	ctxLoader := contextloader.NewLoader(cwd)
	contextText, err := ctxLoader.Load()
	if err != nil {
		return nil, err
	}

	skillCatalog := skills.Discover(skills.DefaultSources(cwd, cfg.Skills.ExtraDirs))
	var persistedDisabled []string
	if persisted, err := skills.LoadDisabledFile(paths.LCoderHome("skills.yaml")); err == nil {
		persistedDisabled = persisted
	}
	applyDisabledLayers(skillCatalog, cfg.Skills.Disabled, persistedDisabled)
	skillsBlock := skillCatalog.Block()

	// Non-fatal capability check: warn if the configured model is known not to
	// support tool calling, since the agent relies on tools.
	if cfg.ModelLacksTools() {
		fmt.Fprintf(os.Stderr, "warning: model %q does not declare the \"tools\" capability; tool calls may fail\n", cfg.Model)
	}

	eng, err := buildEngine(cfg)
	if err != nil {
		return nil, fmt.Errorf("build llm engine: %w", err)
	}
	llmClient := llm.NewClient(eng)

	registry := tools.NewRegistry(cwd)
	if err := registry.RegisterBuiltinFactories(cwd); err != nil {
		return nil, fmt.Errorf("register built-in tools: %w", err)
	}
	registry.Register(skills.UseSkillToolName, builtinTools.NewUseSkill(cwd, skillCatalog))
	mcpConfigs := make([]mcp.ServerConfig, 0, len(cfg.MCPServers))
	for _, s := range cfg.MCPServers {
		mcpConfigs = append(mcpConfigs, mcp.ServerConfig{
			Name:      s.Name,
			Transport: s.Transport,
			Command:   s.Command,
			Env:       s.Env,
			URL:       s.URL,
			Headers:   s.Headers,
			Timeout:   s.Timeout,
		})
	}
	mcpRegistry := mcp.NewRegistry(mcpConfigs)
	if err := mcpRegistry.Connect(); err != nil {
		return nil, fmt.Errorf("mcp connect: %w", err)
	}
	mcpRegistry.RegisterTools(registry)

	permEngine := permissions.NewEngineFromRules(parsePermissionConfig(cfg.Permissions))
	permEngine.SetUnsafeMode(cfg.Permissions.UnsafeMode)
	homeDir, _ := os.UserHomeDir()
	permEngine.SetPathContext(cwd, homeDir)
	_ = permEngine.LoadGlobalLearnedRules(paths.LCoderHome("permissions", "global.yaml"))
	_ = permEngine.LoadProjectRules(filepath.Join(cwd, ".lcoder", "permissions.yaml"))

	sessStore := session.NewStore("")
	var sess *session.Session
	if sessionID != "" {
		sess, err = sessStore.LoadByID(cwd, sessionID)
		if err != nil {
			mcpRegistry.Close()
			return nil, fmt.Errorf("load session: %w", err)
		}
	} else if cont {
		sess, err = sessStore.MostRecent(cwd)
		if err != nil {
			mcpRegistry.Close()
			return nil, fmt.Errorf("continue session: %w", err)
		}
	} else {
		sess, err = sessStore.Create(cwd)
		if err != nil {
			mcpRegistry.Close()
			return nil, fmt.Errorf("create session: %w", err)
		}
	}

	bus := events.New()
	llmClient.OnRetry = func(layer string, attempt int, wait time.Duration, rerr error) {
		_ = bus.Emit(context.Background(), events.LLMRetryEvent{
			Base:    events.Base{Type: events.LLMRetry},
			Layer:   layer,
			Attempt: attempt,
			WaitMs:  wait.Milliseconds(),
			Err:     rerr.Error(),
		})
	}
	obsCfg, err := config.LoadObservabilityConfig("")
	if err != nil {
		mcpRegistry.Close()
		return nil, fmt.Errorf("load observability config: %w", err)
	}
	obsExporter, obsSampler, err := observability.NewExporterFromConfig(obsCfg, sess.ID)
	if err != nil {
		mcpRegistry.Close()
		return nil, fmt.Errorf("observability exporter: %w", err)
	}
	auditLogger, err := observability.NewAuditLoggerFromConfig(obsCfg, sess.ID)
	if err != nil {
		mcpRegistry.Close()
		return nil, fmt.Errorf("audit logger: %w", err)
	}
	obsCollector := observability.NewCollectorWithAudit(obsExporter, sess.ID, auditLogger)
	obsCollector.Subscribe(bus)

	contextSnapshotRecorder := observability.NewContextSnapshotRecorder(sess.ID, obsCfg.ContextSnapshots)

	obsWatcher, err := observability.NewConfigWatcherFromConfig(config.DefaultObservabilityPath(), obsCfg, obsSampler, contextSnapshotRecorder)
	if err != nil {
		mcpRegistry.Close()
		return nil, fmt.Errorf("observability watcher: %w", err)
	}
	if obsWatcher != nil {
		if err := obsWatcher.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to watch observability config: %v\n", err)
		}
	}

	modeManager := agent.NewModeManager()
	modeDirs := agent.DefaultModeDirs(cwd)
	_ = modeManager.LoadModes(modeDirs)
	if modeName == "" {
		modeName = "code"
	}

	window, _ := llmClient.ModelWindow(context.Background(), cfg.Provider, cfg.Model)
	maxOutput, _ := llmClient.ModelMaxOutput(context.Background(), cfg.Provider, cfg.Model)
	maxInput, _ := llmClient.ModelMaxInput(context.Background(), cfg.Provider, cfg.Model)
	budget, source := cfg.ResolveContextBudget(window, maxOutput)
	if source == "default" {
		fmt.Fprintf(os.Stderr, "warning: 未能自动获取模型 %q 的上下文窗口,回退默认 %d\n", cfg.Model, budget.MaxTotal)
	}
	if budget.ClampToMaxInput(maxInput, source) {
		fmt.Fprintf(os.Stderr, "info: 模型 %q prompt 上限 %d 低于上下文窗口,预算按 %d 计算\n", cfg.Model, maxInput, maxInput)
	}
	thinking, thinkWarn := llmClient.ResolveThinking(context.Background(), cfg.Provider, cfg.Model, cfg.Thinking)
	if thinkWarn != "" {
		fmt.Fprintf(os.Stderr, "warning: %s\n", thinkWarn)
	}
	activeSession := agentsetup.NewActiveSession(sess)
	tmplCtx := agentsetup.TemplateContext{
		CWD:   cwd,
		OS:    runtime.GOOS,
		Shell: os.Getenv("SHELL"),
		Now:   time.Now().Format(time.RFC3339),
	}
	mgr := agentsetup.NewContextManager(cfg, budget, thinking, llmClient, contextText, skillsBlock,
		sess.EffectiveMessages(), agentsetup.SessionCompactionSink(activeSession.Get), tmplCtx)

	// before_compact shell hook wraps the built-in summarizer (extension
	// runtime, when present, wraps this in turn at the extBridge site).
	mgr.SetSummarizer(hooks.ShellBeforeCompact(cfg.Hooks.BeforeCompact, sess.ID, mgr.Summarizer()))

	var reminderProducers []agent.ReminderProducer

	var subagentHost *agenthost.Host
	if cfg.Subagent.Enabled {
		profiles, err := subagent.DiscoverAgents(cwd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: load subagent profiles: %v\n", err)
		}
		subagentHost = agenthost.NewHost(agenthost.HostConfig{
			LLMClient:    llmClient,
			Registry:     registry,
			ModeManager:  modeManager,
			Permissions:  permEngine,
			Model:        models.ModelRef{Provider: cfg.Provider, ID: cfg.Model},
			CWD:          cwd,
			HomeDir:      homeDir,
			SessionStore: sessStore,
			Profiles:     profiles,
			ParentBus:    bus,
			NewContextManager: func() *contextmgr.Manager {
				return agentsetup.NewContextManager(cfg, budget, thinking, llmClient, "", "", nil, nil, tmplCtx)
			},
		})
		subagentHost.SetHooks(makeBeforeToolCall(cfg.Hooks, sess.ID), nil)
		subagentHost.SetParentSession(sess.ID)
		subagentTool := builtinTools.NewSubagent(cwd, subagentHost, profiles)
		subagentTool.SetNotifier(func(text string) {
			_ = bus.Emit(context.Background(), events.BackgroundNoticeEvent{
				Base: events.Base{Type: events.BackgroundNotice},
				Text: text,
			})
		})
		registry.Register("subagent", subagentTool)
		// Background subagent completions surface as per-turn reminders.
		reminderProducers = append(reminderProducers, func([]models.AgentMessage) []string {
			return subagentTool.DrainNotifications()
		})
	}

	chkStore := checkpoint.NewFileStore(filepath.Join(session.DefaultDir(), "checkpoints"))
	coreTools := cfg.Context.CoreTools

	agBuilder := agent.NewBuilder().
		WithConfig(agent.Config{
			SystemPrompt:       "",
			BaseSystemPrompt:   agentsetup.BuildSystemPrompt(tmplCtx),
			Model:              models.ModelRef{Provider: cfg.Provider, ID: cfg.Model},
			ContextManager:     mgr,
			BeforeToolCall:     makeBeforeToolCall(cfg.Hooks, sess.ID),
			Mode:               modeName,
			ModeManager:        modeManager,
			DeferredTools:      cfg.Context.DeferredTools,
			CoreTools:          coreTools,
			ReminderProducers:  reminderProducers,
			CheckpointInterval: 1,
			CWD:                cwd,
		}).
		WithGatewayClient(llmClient).
		WithRegistry(registry).
		WithPermissions(permEngine).
		WithEventBus(bus).
		WithObservability(obsCollector).
		WithContextSnapshotRecorder(contextSnapshotRecorder)
	ag, err := agBuilder.
		WithSessionID(sess.ID).
		WithCheckpointStore(chkStore).
		Build()
	if err != nil {
		mcpRegistry.Close()
		return nil, fmt.Errorf("build agent: %w", err)
	}

	// on_stop shell hook: a Stop-hook decider (Claude Code semantics) — exit 2
	// blocks the stop, stderr is steered back to the model.
	ag.AddContinuationDeciders(hooks.OnStopFromConfig(cfg.Hooks, sess.ID, func(reason string) {
		ag.Steer(models.UserMessage("[on_stop hook] " + reason))
	}))

	// Process-external extensions: discover global + project manifests, gate
	// project ones on trust, spawn and handshake, then bridge into the agent.
	extHost, extBridge := startExtensions(cfg, cwd, sess, bus)
	if extBridge != nil {
		before := hooks.CompositeBeforeToolCall(makeBeforeToolCall(cfg.Hooks, sess.ID), extBridge.BeforeToolCall())
		after := hooks.CompositeAfterToolCall(makeAfterToolCall(cfg.Hooks, sess.ID), extBridge.AfterToolCall())
		ag.SetBeforeToolCall(before)
		ag.SetAfterToolCall(after)
		if subagentHost != nil {
			subagentHost.SetHooks(before, after)
		}
		mgr.SetSummarizer(extBridge.Summarizer(mgr.Summarizer()))
		// Extension stop hook joins the continuation chain (Claude Code Stop
		// hook semantics): continue=true blocks the stop, reason steered back.
		ag.AddContinuationDeciders(extBridge.StopDecider(func(reason string) {
			ag.Steer(models.UserMessage("[stop hook] " + reason))
		}))
		// Extension permission hook joins the guard policies (permission.ask).
		ag.AddGuardPolicies(extBridge.PermissionPolicy())
	}

	// The session store owns the message history; load it first. The checkpoint
	// only carries runtime state, so it is applied afterwards without overwriting
	// the conversation.
	ag.SetMessages(sess.EffectiveMessages())
	resumed := false
	if cp, err := chkStore.Load(sess.ID); err == nil {
		if err := ag.Restore(cp); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to restore checkpoint: %v; continuing with session messages only\n", err)
		} else {
			resumed = true
		}
	}

	// session_start hooks inject their context once the agent is ready.
	if extBridge != nil {
		if extra := extBridge.SessionStart(context.Background(), sess.ID, resumed); extra != "" {
			ag.Steer(models.UserMessage("[session_start hook] " + extra))
		}
	}

	return &agentSetup{
		ag:              ag,
		sess:            sess,
		activeSession:   activeSession,
		store:           sessStore,
		bus:             bus,
		mcpRegistry:     mcpRegistry,
		cfg:             agentConfig{Config: cfg, skillCatalog: skillCatalog, modeManager: modeManager},
		cwd:             cwd,
		llmClient:       llmClient,
		checkpointStore: chkStore,
		obsWatcher:      obsWatcher,
		subagentHost:    subagentHost,
		extHost:         extHost,
		extBridge:       extBridge,
		cleanup: func() {
			if obsWatcher != nil {
				_ = obsWatcher.Close()
			}
			if extHost != nil {
				extHost.Close()
			}
			_ = bus.Close()
			obsCollector.Close()
			mcpRegistry.Close()
			_ = eng.Close()
		},
	}, nil
}

func runRoot(cmd *cobra.Command, args []string) error {
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

	if promptText == "" && len(args) > 0 {
		promptText = strings.Join(args, " ")
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), shutdownSignals...)
	defer stop()

	var runErr error
	if jsonMode {
		runErr = runJSONMode(ctx, setup, promptText)
	} else if promptText != "" {
		runErr = runOneShot(ctx, setup, promptText)
	} else {
		runErr = runTUI(ctx, setup)
	}

	if ctx.Err() != nil {
		writeCrashCheckpoint(setup)
	} else if runErr == nil {
		writeBestEffortCheckpoint(setup, checkpoint.ReasonManual)
	}
	return runErr
}

// writeBestEffortCheckpoint captures and persists the current agent state.
// It is used for both crash recovery and clean-exit snapshots. Errors are
// logged to stderr and do not affect the exit path.
func writeBestEffortCheckpoint(setup *agentSetup, reason string) {
	if setup.checkpointStore == nil {
		return
	}
	sessionID := setup.ag.SessionID()
	if sessionID == "" {
		return
	}
	cp, err := setup.ag.CheckpointWithReason(reason)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to capture %s checkpoint: %v\n", reason, err)
		return
	}
	if err := setup.checkpointStore.Save(sessionID, cp); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to save %s checkpoint: %v\n", reason, err)
	}
}

// writeCrashCheckpoint captures the current agent state as a crash checkpoint
// when the process is interrupted. It is best-effort: if the agent is mid-turn,
// the checkpoint may reflect a partial turn, but it still preserves more state
// than losing everything since the last auto-checkpoint.
func writeCrashCheckpoint(setup *agentSetup) {
	writeBestEffortCheckpoint(setup, checkpoint.ReasonCrash)
}

func runJSONMode(ctx context.Context, setup *agentSetup, prompt string) error {
	setup.ag.SetUserConfirm(cliConfirm{})
	if setup.subagentHost != nil {
		setup.subagentHost.SetUserConfirm(cliConfirm{})
	}
	if setup.subagentHost != nil {
		setup.subagentHost.SetUserConfirm(cliConfirm{})
	}
	var msg models.AgentMessage
	if prompt != "" {
		// Slash-prefixed input (extension/builtin commands, manual skill
		// triggers) bypasses the input hook, matching the TUI.
		if setup.extBridge != nil && !strings.HasPrefix(prompt, "/") {
			newText, proceed, reason := setup.extBridge.InputHook(ctx, prompt)
			if !proceed {
				return fmt.Errorf("input blocked: %s", reason)
			}
			prompt = newText
		}
		msg = models.NewAgentMessage(models.RoleUser, models.TextContent{Text: prompt})
		if err := setup.sess.Append(msg); err != nil {
			return fmt.Errorf("append message: %w", err)
		}
	}

	jsonHandler := func(ctx context.Context, ev events.Event) error {
		data, err := events.MarshalJSON(ev)
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}
	unsub := setup.bus.Subscribe(jsonHandler)
	defer unsub()

	if prompt != "" {
		if err := setup.ag.Prompt(ctx, msg); err != nil {
			return err
		}
	} else {
		if err := setup.ag.Continue(ctx); err != nil {
			return err
		}
	}
	return nil
}

func runOneShot(ctx context.Context, setup *agentSetup, prompt string) error {
	setup.ag.SetUserConfirm(cliConfirm{})
	fmt.Printf("[lcoder] session=%s mode=%s\n", setup.sess.ID, modeName)

	// Persist after each assistant/tool message turn.
	persistHandler := func(ctx context.Context, ev events.Event) error {
		switch ev.(type) {
		// Compactions are persisted by the context manager's CompactionSink
		// (agentsetup.SessionCompactionSink), inside the same call that folds the
		// context — not from here, where a missed event would silently leave the
		// session claiming the folded messages are still active.
		case events.TurnEndEvent, events.AgentEndEvent:
			// Mirror the completed turn's assistant/tool messages into the
			// session now. TurnEnd is dispatched synchronously from the agent
			// loop, which writes the automatic checkpoint right afterwards —
			// persisting here keeps the session on disk at least as new as any
			// checkpoint, so a crash cannot resurrect a checkpoint whose
			// messages were never saved.
			_ = setup.sess.AppendMissing(setup.ag.AllMessages())
		}
		return nil
	}
	unsub := setup.bus.Subscribe(persistHandler)
	defer unsub()

	// Slash-prefixed input (extension/builtin commands, manual skill triggers)
	// bypasses the input hook, matching the TUI.
	if setup.extBridge != nil && !strings.HasPrefix(prompt, "/") {
		newText, proceed, reason := setup.extBridge.InputHook(ctx, prompt)
		if !proceed {
			return fmt.Errorf("input blocked: %s", reason)
		}
		prompt = newText
	}
	msg := models.NewAgentMessage(models.RoleUser, models.TextContent{Text: prompt})
	// A manual "/skill:name args" trigger folds the skill body into the user
	// message; the model can also activate skills on its own via use_skill.
	if name, rest, ok := skills.ParseManualTrigger(prompt); ok {
		meta, found := setup.cfg.skillCatalog.Find(name)
		if !found {
			return fmt.Errorf("skill %q not found", name)
		}
		skill, err := skills.LoadSkill(meta.Source)
		if err != nil {
			return fmt.Errorf("load skill %q: %w", name, err)
		}
		msg = skills.ExpandManualTrigger(skill, rest)
	}
	if err := setup.sess.Append(msg); err != nil {
		return fmt.Errorf("append message: %w", err)
	}

	if err := setup.ag.Prompt(ctx, msg); err != nil {
		return err
	}
	final := setup.ag.AllMessages()
	// Mirror the agent's assistant/tool output into the session so every
	// message reaches disk, not just the user prompts appended above.
	if err := setup.sess.AppendMissing(final); err != nil {
		return fmt.Errorf("persist session: %w", err)
	}
	if len(final) == 0 {
		return nil
	}
	fmt.Println(final[len(final)-1].Text())
	return nil
}

func runTUI(ctx context.Context, setup *agentSetup) error {
	modelRef := setup.cfg.Provider + "/" + setup.cfg.Model

	var caps []string
	if meta, ok := setup.cfg.ModelMeta(); ok {
		caps = meta.Capabilities
	}
	needsSetup := !config.ProviderHasKey(setup.cfg.Config, setup.cfg.Provider)

	// Extension input hook and slash commands must be installed before the
	// bubbletea program loop begins (RegisterExtensionCommand is not safe for
	// concurrent use with the running TUI).
	if setup.extBridge != nil {
		tui.SetInputHook(func(text string) (string, bool, string) {
			return setup.extBridge.InputHook(ctx, text)
		})
	}
	if setup.extHost != nil {
		for _, c := range setup.extHost.Commands() {
			if err := tui.RegisterExtensionCommand(c.Decl.Name, c.Decl.Description, c.Decl.Usage, func(args string) string {
				out, err := setup.extHost.InvokeCommand(context.Background(), c.Decl.Name, args)
				if err != nil {
					return "error: " + err.Error()
				}
				return out
			}); err != nil {
				fmt.Fprintf(os.Stderr, "warning: %v\n", err)
			}
		}
	}

	return tui.Run(setup.bus, setup.ag, setup.sess, setup.store, setup.cwd, modelRef, setup.cfg.TUI.Theme, setup.mcpRegistry, setup.cfg.modeManager, caps, setup.llmClient, setup.cfg.Config, needsSetup,
		func(s *session.Session) {
			setup.activeSession.Set(s)
			if setup.subagentHost != nil {
				setup.subagentHost.SetParentSession(s.ID)
			}
		},
		setup.subagentHost,
		setup.cfg.skillCatalog)
}
