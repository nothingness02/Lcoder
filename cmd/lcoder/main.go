package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/agentsetup"
	"github.com/lcoder/lcoder/pkg/checkpoint"
	"github.com/lcoder/lcoder/pkg/config"
	contextloader "github.com/lcoder/lcoder/pkg/context"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/extension"
	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/memory"
	"github.com/lcoder/lcoder/pkg/mcp"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/observability"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/sandbox"
	"github.com/lcoder/lcoder/pkg/session"
	"github.com/lcoder/lcoder/pkg/skills"
	"github.com/lcoder/lcoder/pkg/tools"
	builtinTools "github.com/lcoder/lcoder/pkg/tools/builtin"
	"github.com/lcoder/lcoder/pkg/tui"
)

var (
	cfgFile    string
	modelID    string
	provider   string
	sessionID  string
	cont       bool
	modeName   string
	promptText string
)

func main() {
	root := &cobra.Command{
		Use:   "lcoder [prompt]",
		Short: "Lcoder — a minimal, extensible SWE agent",
		RunE:  runRoot,
	}

	root.Flags().StringVar(&cfgFile, "config", "", "Path to config file")
	root.Flags().StringVar(&modelID, "model", "", "Model ID")
	root.Flags().StringVar(&provider, "provider", "", "Model provider")
	root.Flags().StringVar(&sessionID, "session", "", "Session ID to resume")
	root.Flags().BoolVarP(&cont, "continue", "c", false, "Continue most recent session")
	root.Flags().StringVar(&modeName, "mode", "", "Agent mode: plan, code, explore, review, test")
	root.Flags().StringVarP(&promptText, "prompt", "p", "", "Single prompt to run and exit")
	root.Flags().Bool("json", false, "Output events as JSONL instead of TUI/text")

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
	return cfg, nil
}

type agentSetup struct {
	ag             *agent.Agent
	sess           *session.Session
	store          *session.Store
	bus            *events.Bus
	mcpRegistry    *mcp.Registry
	cfg            agentConfig
	cwd            string
	llmClient      *llm.Client
	checkpointStore checkpoint.Store
	obsWatcher     *observability.ConfigWatcher
	cleanup        func()
}

type agentConfig struct {
	config.Config
	loadedSkills []skills.Skill
	modeManager  *agent.ModeManager
}

func prepareAgent(cfg config.Config, cwd string) (*agentSetup, error) {
	ctxLoader := contextloader.NewLoader(cwd)
	contextText, err := ctxLoader.Load()
	if err != nil {
		return nil, err
	}

	extMgr := extension.DefaultManager()

	skillPaths := append(skills.DefaultPaths(cwd), extMgr.SkillDirs()...)
	loadedSkills, _ := skills.Load(skillPaths)
	skillsBlock := skills.ToSystemPromptBlock(loadedSkills)

	// Non-fatal capability check: warn if the configured model is known not to
	// support tool calling, since the agent relies on tools.
	if cfg.ModelLacksTools() {
		fmt.Fprintf(os.Stderr, "warning: model %q does not declare the \"tools\" capability; tool calls may fail\n", cfg.Model)
	}

	llmClient := llm.NewClient(buildEngine(cfg))

	sb, err := sandbox.New(toSandboxConfig(cfg.Sandbox, cwd))
	if err != nil {
		return nil, fmt.Errorf("init sandbox: %w", err)
	}

	var memStore *memory.Store
	if cfg.Memory.Enabled {
		memStore, err = memory.NewStore(cwd)
		if err != nil {
			return nil, fmt.Errorf("init memory store: %w", err)
		}
		memStore.WithLimits(memory.Limits{
			MemoryCharLimit: cfg.Memory.MemoryCharLimit,
			UserCharLimit:   cfg.Memory.UserCharLimit,
		})
	}

	registry := tools.NewRegistry(cwd)
	registry.SetSandbox(sb)
	if err := registry.RegisterBuiltinFactories(cwd); err != nil {
		return nil, fmt.Errorf("register built-in tools: %w", err)
	}
	if memStore != nil {
		registry.Register("memory", builtinTools.NewMemory(cwd, memStore))
	}
	for _, cfgTool := range cfg.HTTPTools {
		registry.Register(cfgTool.Name, tools.NewHTTPExecutable(tools.HTTPConfig{
			Name:          cfgTool.Name,
			Endpoint:      tools.ExpandEndpointEnv(cfgTool.Endpoint),
			Description:   cfgTool.Description,
			Parameters:    cfgTool.Parameters,
			ExecutionMode: models.ExecutionMode(cfgTool.ExecutionMode),
			Headers:       cfgTool.Headers,
		}))
	}

	mcpConfigs := make([]mcp.ServerConfig, 0, len(cfg.MCPServers))
	for _, s := range cfg.MCPServers {
		mcpConfigs = append(mcpConfigs, mcp.ServerConfig{
			Name:    s.Name,
			Command: s.Command,
			Env:     s.Env,
		})
	}
	mcpRegistry := mcp.NewRegistry(mcpConfigs)
	if err := mcpRegistry.Connect(); err != nil {
		return nil, fmt.Errorf("mcp connect: %w", err)
	}
	mcpRegistry.RegisterTools(registry)

	permEngine := permissions.NewEngineFromRules(parsePermissionConfig(cfg.Permissions))

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

	obsWatcher, err := observability.NewConfigWatcherFromConfig(config.DefaultObservabilityPath(), obsCfg, obsSampler)
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
	modeDirs := append(agent.DefaultModeDirs(cwd), extMgr.AgentDirs()...)
	_ = modeManager.LoadModes(modeDirs)
	if modeName == "" {
		modeName = "code"
	}

	window, _ := llmClient.ModelWindow(context.Background(), cfg.Provider, cfg.Model)
	maxOutput, _ := llmClient.ModelMaxOutput(context.Background(), cfg.Provider, cfg.Model)
	budget, source := cfg.ResolveContextBudget(window, maxOutput)
	if source == "default" {
		fmt.Fprintf(os.Stderr, "warning: 未能自动获取模型 %q 的上下文窗口,回退默认 %d\n", cfg.Model, budget.MaxTotal)
	}
	mgr := agentsetup.NewContextManager(cfg, budget, llmClient, contextText, skillsBlock, sess.ActiveMessages(), memStore)
	chkStore := checkpoint.NewFileStore(filepath.Join(session.DefaultDir(), "checkpoints"))
	ag, err := agent.NewBuilder().
		WithConfig(agent.Config{
			SystemPrompt:      "",
			BaseSystemPrompt:  agentsetup.BuildSystemPrompt(),
			Model:             models.ModelRef{Provider: cfg.Provider, ID: cfg.Model},
			ToolExecutionMode: models.ExecutionParallel,
			ContextManager:    mgr,
			BeforeToolCall:    makeBeforeToolCall(cfg.Hooks),
			Mode:              modeName,
			ModeManager:       modeManager,
			DeferredTools:     cfg.Context.DeferredTools,
			CoreTools:         cfg.Context.CoreTools,
		}).
		WithGatewayClient(llmClient).
		WithRegistry(registry).
		WithPermissions(permEngine).
		WithEventBus(bus).
		WithObservability(obsCollector).
		WithSessionID(sess.ID).
		WithCheckpointStore(chkStore).
		Build()
	if err != nil {
		mcpRegistry.Close()
		return nil, fmt.Errorf("build agent: %w", err)
	}

	// The session store owns the message history; load it first. The checkpoint
	// only carries runtime state, so it is applied afterwards without overwriting
	// the conversation.
	ag.SetMessages(sess.ActiveMessages())
	if cp, err := chkStore.Load(sess.ID); err == nil {
		if err := ag.Restore(cp); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to restore checkpoint: %v; continuing with session messages only\n", err)
		}
	}

	return &agentSetup{
		ag:              ag,
		sess:            sess,
		store:           sessStore,
		bus:             bus,
		mcpRegistry:     mcpRegistry,
		cfg:             agentConfig{Config: cfg, loadedSkills: loadedSkills},
		cwd:             cwd,
		llmClient:       llmClient,
		checkpointStore: chkStore,
		obsWatcher:      obsWatcher,
		cleanup: func() {
			if obsWatcher != nil {
				_ = obsWatcher.Close()
			}
			obsCollector.Close()
			mcpRegistry.Close()
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

	jsonMode, _ := cmd.Flags().GetBool("json")

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
	}
	return runErr
}

// writeCrashCheckpoint captures the current agent state as a crash checkpoint
// when the process is interrupted. It is best-effort: if the agent is mid-turn,
// the checkpoint may reflect a partial turn, but it still preserves more state
// than losing everything since the last auto-checkpoint.
func writeCrashCheckpoint(setup *agentSetup) {
	if setup.checkpointStore == nil || setup.sess == nil {
		return
	}
	cp, err := setup.ag.CheckpointWithReason(checkpoint.ReasonCrash)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to capture crash checkpoint: %v\n", err)
		return
	}
	if err := setup.checkpointStore.Save(setup.sess.ID, cp); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to save crash checkpoint: %v\n", err)
	}
}

func runJSONMode(ctx context.Context, setup *agentSetup, prompt string) error {
	setup.ag.SetUserConfirm(cliConfirm{})
	var msg models.AgentMessage
	if prompt != "" {
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
		case events.CompactionCommittedEvent:
			// Compaction committed in the manager: reset the on-disk session to
			// the compacted runtime state (summary + recent tail), discarding the
			// older raw messages.
			_ = setup.sess.Replace(setup.ag.AllMessages())
		case events.MessageEndEvent, events.ToolExecutionEndEvent, events.AgentEndEvent:
			_ = setup.sess.Save()
		}
		return nil
	}
	unsub := setup.bus.Subscribe(persistHandler)
	defer unsub()

	var initialMessages []models.AgentMessage
	if name, rest, ok := skills.ParseManualTrigger(prompt); ok {
		if skill, found := skills.FindByName(setup.cfg.loadedSkills, name); found {
			initialMessages = skills.ExpandManualTrigger(skill, rest)
		} else {
			return fmt.Errorf("skill %q not found", name)
		}
	} else if setup.cfg.Context.Mode == "auto" {
		// Auto-detect skill from prompt when no manual trigger is used.
		if score, ok := skills.AutoDetect(prompt, setup.cfg.loadedSkills); ok {
			fmt.Printf("[lcoder] auto-activated skill: %s\n", score.Skill.Name)
			initialMessages = skills.ExpandManualTrigger(score.Skill, prompt)
		}
	}

	var msg models.AgentMessage
	if len(initialMessages) > 0 {
		for _, m := range initialMessages {
			if err := setup.sess.Append(m); err != nil {
				return fmt.Errorf("append message: %w", err)
			}
		}
		msg = initialMessages[len(initialMessages)-1]
	} else {
		msg = models.NewAgentMessage(models.RoleUser, models.TextContent{Text: prompt})
		if err := setup.sess.Append(msg); err != nil {
			return fmt.Errorf("append message: %w", err)
		}
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
	httpTools := make([]tui.HTTPToolItem, 0, len(setup.cfg.HTTPTools))
	for _, t := range setup.cfg.HTTPTools {
		httpTools = append(httpTools, tui.HTTPToolItem{
			Name:        t.Name,
			Endpoint:    t.Endpoint,
			Description: t.Description,
		})
	}
	modelRef := setup.cfg.Provider + "/" + setup.cfg.Model

	// Persist after each assistant/tool message turn.
	persistHandler := func(ctx context.Context, ev events.Event) error {
		switch ev.(type) {
		case events.CompactionCommittedEvent:
			_ = setup.sess.Replace(setup.ag.AllMessages())
		case events.MessageEndEvent, events.ToolExecutionEndEvent, events.AgentEndEvent:
			_ = setup.sess.Save()
		}
		return nil
	}
	unsub := setup.bus.Subscribe(persistHandler)
	defer unsub()

	var caps []string
	if meta, ok := setup.cfg.ModelMeta(); ok {
		caps = meta.Capabilities
	}
	needsSetup := !config.ProviderHasKey(setup.cfg.Config, setup.cfg.Provider)
	return tui.Run(setup.bus, setup.ag, setup.sess, setup.store, setup.cwd, modelRef, setup.cfg.TUI.Theme, httpTools, setup.mcpRegistry, setup.cfg.modeManager, caps, setup.llmClient, setup.cfg.Config, needsSetup, setup.cfg.loadedSkills...)
}
