package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/extension"
	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/observability"
	"github.com/lcoder/lcoder/pkg/session"
	"github.com/lcoder/lcoder/pkg/skills"
)

func modelsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "models",
		Short: "List available models from the catalog",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			client := llm.NewClient(buildEngine(cfg))
			models, err := client.ListModels(cmd.Context())
			if err != nil {
				return err
			}
			for _, m := range models {
				fmt.Println(m.ID)
			}
			return nil
		},
	}
}

func skillsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "skills",
		Short: "List loaded skills and their prompts",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			paths := append(skills.DefaultPaths(cwd), extension.DefaultManager().SkillDirs()...)
			catalog, err := skills.LoadCatalog(paths)
			if err != nil {
				return err
			}
			for _, s := range catalog {
				fmt.Printf("- %s: %s\n", s.Name, s.Description)
			}
			return nil
		},
	}
}

func sessionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sessions",
		Short: "List sessions for the current workspace",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			store := session.NewStore("")
			sessions, err := store.List(cwd)
			if err != nil {
				return err
			}
			for _, s := range sessions {
				fmt.Printf("%s %s\n", s.ID, time.Unix(s.CreatedAt, 0).Format("2006-01-02 15:04"))
			}
			return nil
		},
	}
}

func modesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "modes",
		Short: "List available agent modes",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			mm := agent.NewModeManager()
			dirs := append(agent.DefaultModeDirs(cwd), extension.DefaultManager().AgentDirs()...)
			_ = mm.LoadModes(dirs)
			for _, mode := range mm.List() {
				fmt.Printf("- %s: %s\n", mode.Name, mode.Description)
			}
			return nil
		},
	}
}

func statsCmd() *cobra.Command {
	var obsPath string
	cmd := &cobra.Command{
		Use:   "stats --session SESSION",
		Short: "Show observability stats for a session",
		RunE: func(cmd *cobra.Command, args []string) error {
			if obsPath == "" {
				if len(args) == 0 {
					return fmt.Errorf("session id required")
				}
				obsPath = observability.DefaultPath(args[0])
			}
			stats, err := observability.SummarizeFile(obsPath)
			if err != nil {
				return err
			}
			fmt.Printf("turns: %d\n", stats.Turns)
			fmt.Printf("input tokens: %d\n", stats.InputTokens)
			fmt.Printf("output tokens: %d\n", stats.OutputTokens)
			fmt.Printf("total cost: $%.6f\n", stats.TotalCost)
			return nil
		},
	}
	cmd.Flags().StringVar(&obsPath, "file", "", "Observability JSONL file")
	return cmd
}

func exportCmd() *cobra.Command {
	var format, output string
	cmd := &cobra.Command{
		Use:   "export --session SESSION",
		Short: "Export session observability to html/sqlite/prometheus",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("session id required")
			}
			path := observability.DefaultPath(args[0])
			var exporter observability.Exporter
			switch format {
			case "sqlite":
				var err error
				exporter, err = observability.NewSQLiteExporter(output)
				if err != nil {
					return err
				}
			case "html":
				exporter = observability.NewHTMLExporter()
			case "markdown":
				exporter = observability.NewMarkdownExporter()
			case "prometheus":
				exporter = observability.NewPrometheusExporter()
			default:
				return fmt.Errorf("unknown format: %s", format)
			}
			return observability.ExportFile(path, exporter, output)
		},
	}
	cmd.Flags().StringVar(&format, "format", "html", "Export format: html, markdown, sqlite, prometheus")
	cmd.Flags().StringVarP(&output, "output", "o", "lcoder-export", "Output file or directory")
	return cmd
}

func traceCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "trace SESSION",
		Short: "Print formatted trace for a session",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("session id required")
			}
			path := observability.DefaultPath(args[0])
			trace, err := observability.FormatTrace(path)
			if err != nil {
				return err
			}
			fmt.Println(trace)
			return nil
		},
	}
}

func metricsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "metrics",
		Short: "Run Prometheus metrics endpoint",
		RunE: func(cmd *cobra.Command, args []string) error {
			port := "9090"
			if len(args) > 0 {
				port = args[0]
			}
			return observability.ServeMetrics(port)
		},
	}
	return cmd
}

func tuiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Start interactive TUI",
		// No positional args: they would be read as a one-shot prompt by
		// runRoot. promptText is also forced empty so this command always
		// lands in runTUI.
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			promptText = ""
			return runRoot(cmd, args)
		},
	}
}

func installCmd() *cobra.Command {
	var name string
	var local bool
	cmd := &cobra.Command{
		Use:   "install SOURCE",
		Short: "Install an extension or package from a local path or git repo",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			source := args[0]
			if local {
				abs, err := filepath.Abs(source)
				if err != nil {
					return err
				}
				source = abs
			}
			if name == "" {
				name = guessName(source)
			}
			mgr := extension.DefaultManager()
			pkg, err := mgr.InstallPackage(name, source)
			if err != nil {
				// Try as a Go extension.
				loader := extension.DefaultLoader()
				dir, err2 := loader.Install(name, source)
				if err2 != nil {
					return fmt.Errorf("install package: %w\ninstall extension: %w", err, err2)
				}
				fmt.Printf("Installed extension %s at %s\n", name, dir)
				return nil
			}
			fmt.Printf("Installed package %s v%s at %s\n", pkg.Info.Name, pkg.Info.Version, pkg.RootDir)
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "name", "n", "", "Name for the installed extension/package")
	cmd.Flags().BoolVarP(&local, "local", "l", false, "Force treat SOURCE as a local path")
	return cmd
}

func uninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall NAME",
		Short: "Uninstall an extension or package",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			mgr := extension.DefaultManager()
			var errs []error
			if err := mgr.UninstallPackage(name); err != nil {
				errs = append(errs, err)
			}
			if err := extension.DefaultLoader().Uninstall(name); err != nil {
				errs = append(errs, err)
			}
			if len(errs) == 2 {
				return fmt.Errorf("uninstall failed: %v", errs)
			}
			fmt.Printf("Uninstalled %s\n", name)
			return nil
		},
	}
}

func listExtensionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list-extensions",
		Short: "List installed extensions and packages",
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr := extension.DefaultManager()
			pkgs, err := mgr.ListPackages()
			if err != nil {
				return err
			}
			if len(pkgs) > 0 {
				fmt.Println("Packages:")
				for _, p := range pkgs {
					fmt.Printf("- %s v%s (%s)\n", p.Info.Name, p.Info.Version, p.RootDir)
				}
			}
			exts, err := extension.DefaultLoader().List()
			if err != nil {
				return err
			}
			if len(exts) > 0 {
				fmt.Println("Extensions:")
				for _, e := range exts {
					fmt.Printf("- %s\n", e)
				}
			}
			if len(pkgs) == 0 && len(exts) == 0 {
				fmt.Println("No extensions or packages installed.")
			}
			return nil
		},
	}
}

func updateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update NAME",
		Short: "Update an installed extension or package",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			// For local extensions, update is a no-op. For git, reinstall.
			loader := extension.DefaultLoader()
			dir, err := loader.LoadPath(name)
			if err != nil {
				return fmt.Errorf("extension not found: %w", err)
			}
			// Detect if it's a git repo.
			gitDir := filepath.Join(dir, ".git")
			if _, err := os.Stat(gitDir); err == nil {
				if err := loader.Uninstall(name); err != nil {
					return err
				}
				// Re-clone from existing remote.
				out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
				if err != nil {
					return fmt.Errorf("get remote url: %w", err)
				}
				remote := strings.TrimSpace(string(out))
				newDir, err := loader.Install(name, remote)
				if err != nil {
					return err
				}
				fmt.Printf("Updated extension %s at %s\n", name, newDir)
				return nil
			}
			fmt.Printf("%s is a local extension; update is not supported. Reinstall if needed.\n", name)
			return nil
		},
	}
}

func guessName(source string) string {
	base := filepath.Base(source)
	base = strings.TrimSuffix(base, ".git")
	if base == "" || base == "." {
		return "unknown"
	}
	return base
}
