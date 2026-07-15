package subagent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

const (
	maxParallelTasks    = 8
	parallelConcurrency = 4
)

// Invocation is a single subagent invocation.
type Invocation struct {
	Agent string
	Task  string
	CWD   string
}

// TaskItem is one task in a parallel invocation.
type TaskItem = Invocation

// ChainItem is one step in a chain invocation.
type ChainItem = Invocation

// Result is the outcome of one parallel task.
type Result struct {
	Text string
	Err  error
}

// Runner executes subagent invocations.
type Runner interface {
	RunSingle(ctx context.Context, agentName string, task string, cwd string) (string, error)
	RunParallel(ctx context.Context, items []TaskItem) ([]Result, error)
	RunChain(ctx context.Context, items []ChainItem) (string, error)
}

// DefaultRunner runs subagents by invoking the lcoder CLI.
type DefaultRunner struct {
	projectRoot string
	agents      map[string]Agent
	LCoderPath  string
}

// NewRunner creates a Runner for the given project root.
func NewRunner(projectRoot string) (Runner, error) {
	agents, err := DiscoverAgents(projectRoot)
	if err != nil {
		return nil, err
	}
	return &DefaultRunner{
		projectRoot: projectRoot,
		agents:      agents,
		LCoderPath:  "lcoder",
	}, nil
}

func (r *DefaultRunner) resolveAgent(name string) (Agent, error) {
	agent, ok := r.agents[name]
	if !ok {
		return Agent{}, UnknownAgentError{Name: name}
	}
	return agent, nil
}

func (r *DefaultRunner) validateCWD(cwd string) (string, error) {
	if cwd == "" {
		return r.projectRoot, nil
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("subagent: resolve cwd: %w", err)
	}
	rel, err := filepath.Rel(r.projectRoot, abs)
	if err != nil {
		return "", fmt.Errorf("subagent: cwd %q is outside project root %q", cwd, r.projectRoot)
	}
	parentPrefix := ".." + string(filepath.Separator)
	if rel == ".." || strings.HasPrefix(rel, parentPrefix) {
		return "", fmt.Errorf("subagent: cwd %q is outside project root %q", cwd, r.projectRoot)
	}
	return abs, nil
}

func (r *DefaultRunner) RunSingle(ctx context.Context, agentName string, task string, cwd string) (string, error) {
	agent, err := r.resolveAgent(agentName)
	if err != nil {
		return "", err
	}
	workDir, err := r.validateCWD(cwd)
	if err != nil {
		return "", err
	}
	args := buildInvocationArgs(agent, task)
	out, err := runSubprocess(ctx, r.LCoderPath, args, workDir, time.Duration(agent.Timeout)*time.Second)
	if err != nil {
		return "", err
	}
	return ExtractFinalAnswer(out)
}

func (r *DefaultRunner) RunParallel(ctx context.Context, items []TaskItem) ([]Result, error) {
	if len(items) > maxParallelTasks {
		return nil, fmt.Errorf("subagent: too many parallel tasks (%d > %d)", len(items), maxParallelTasks)
	}
	results := make([]Result, len(items))
	g, ctx := errgroup.WithContext(ctx)
	sem := make(chan struct{}, parallelConcurrency)
	for i, item := range items {
		i, item := i, item
		g.Go(func() error {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i] = Result{Err: ctx.Err()}
				return nil
			}
			defer func() { <-sem }()
			text, err := r.RunSingle(ctx, item.Agent, item.Task, item.CWD)
			results[i] = Result{Text: text, Err: err}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return results, nil
}

func (r *DefaultRunner) RunChain(ctx context.Context, items []ChainItem) (string, error) {
	previous := ""
	for _, item := range items {
		task := strings.ReplaceAll(item.Task, "{previous}", previous)
		text, err := r.RunSingle(ctx, item.Agent, task, item.CWD)
		if err != nil {
			return "", err
		}
		previous = text
	}
	return previous, nil
}
