package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/llm/provider"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/task"
	"github.com/lcoder/lcoder/pkg/tools"
	_ "github.com/lcoder/lcoder/pkg/tools/builtin" // registers builtin tool factories via init()
)

// todoCall builds an assistant message that calls todo_write with the given tasks.
func todoCall(callID string, todos []task.Task) models.AgentMessage {
	items := make([]any, 0, len(todos))
	for _, t := range todos {
		items = append(items, map[string]any{
			"text":   t.Text,
			"status": string(t.Status),
		})
	}
	return models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Type:      "tool_call",
		ID:        callID,
		Name:      task.ToolName,
		Arguments: map[string]any{"todos": items},
	})
}

// tasksAt returns the task list for a given turn in the scripted scenario.
// Turns 0..4 each advance one task to in_progress; turn 5 marks everything done.
func tasksAt(turn int) []task.Task {
	all := []string{"analyze requirements", "design schema", "implement core", "write tests", "deploy service"}
	if turn >= len(all) {
		var done []task.Task
		for _, text := range all {
			done = append(done, task.Task{Text: text, Status: task.StatusDone})
		}
		return done
	}
	status := func(i int) task.Status {
		switch {
		case i < turn:
			return task.StatusDone
		case i == turn:
			return task.StatusInProgress
		default:
			return task.StatusPending
		}
	}
	var out []task.Task
	for i, text := range all {
		out = append(out, task.Task{Text: text, Status: status(i)})
	}
	return out
}

// taskMetrics holds the observed todo_write usage and derived statistics.
type taskMetrics struct {
	TotalTurns        int
	TotalCalls        int
	CallsPerTurn      []int
	StatusHistory     []statusCounts
	TotalTasks        int
	DoneTasks         int
	AvgCompletionTurn float64
	MaxPendingStreak  int
	UnfinishedTasks   []string
	TaskTimelines     map[string][]task.Status
}

type statusCounts struct {
	Turn       int
	Pending    int
	InProgress int
	Done       int
}

type taskObservation struct {
	turn  int
	tasks []task.Task
}

// TestTasksToolFrequencyAndLongRangeMetrics drives a scripted 6-turn agent run
// where todo_write is invoked every turn. It records invocation frequency and
// tracks how tasks move through pending/in_progress/done, then writes a
// markdown visualization of the metrics.
func TestTasksToolFrequencyAndLongRangeMetrics(t *testing.T) {
	const turns = 6

	// Build a deterministic LLM script: each turn the model calls todo_write
	// with one more task marked done and the next one in progress; the final
	// turn marks every task done.
	var scripts [][]provider.Event
	for i := 0; i < turns; i++ {
		scripts = append(scripts, llmtest.Turn(llmtest.Done(todoCall(fmt.Sprintf("call_%d", i), tasksAt(i)), nil)))
	}
	client := llmtest.Client(scripts...)

	registry := tools.NewRegistry(".")
	if err := registry.RegisterBuiltinFactories("."); err != nil {
		t.Fatalf("register builtin tools: %v", err)
	}

	bus := events.New()
	var callsSeen int
	var calls []taskObservation
	bus.Subscribe(func(_ context.Context, ev events.Event) error {
		if e, ok := ev.(events.ToolExecutionStartEvent); ok && e.ToolName == task.ToolName {
			todos, err := task.Parse(e.Args["todos"])
			if err != nil {
				return err
			}
			calls = append(calls, taskObservation{turn: e.Turn, tasks: todos})
		}
		return nil
	})

	ag := agent.New(agent.Config{
		SystemPrompt: "You are a helpful assistant.",
		Model:        models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ShouldStop: func(_ context.Context, ts agent.TurnSummary) (bool, error) {
			for _, tc := range ts.Message.ToolCalls() {
				if tc.Name == task.ToolName {
					callsSeen++
					break
				}
			}
			return callsSeen >= turns, nil
		},
	}, client, registry, permissions.NewEngine(permissions.DefaultConfig()), bus)

	if err := ag.Prompt(context.Background(), models.UserMessage("Plan and execute a small project.")); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	metrics := computeTaskMetrics(calls, turns)
	md := renderTaskMetricsMarkdown(metrics)

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	outDir := filepath.Join(wd, "output")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir output: %v", err)
	}
	mdPath := filepath.Join(outDir, "tasks_metrics.md")
	if err := os.WriteFile(mdPath, []byte(md), 0o644); err != nil {
		t.Fatalf("write markdown report: %v", err)
	}
	t.Logf("tasks metrics markdown: %s", mdPath)

	if metrics.TotalCalls != turns {
		t.Fatalf("expected %d todo_write calls, got %d", turns, metrics.TotalCalls)
	}
	if metrics.DoneTasks != metrics.TotalTasks {
		t.Fatalf("expected %d completed tasks, got %d", metrics.TotalTasks, metrics.DoneTasks)
	}
	if metrics.AvgCompletionTurn == 0 {
		t.Fatal("expected non-zero average completion turn")
	}
}

func computeTaskMetrics(calls []taskObservation, turns int) taskMetrics {
	callsPerTurn := make([]int, turns)
	lastByTurn := make([][]task.Task, turns)
	firstDone := map[string]int{}

	for _, c := range calls {
		if c.turn < 0 || c.turn >= turns {
			continue
		}
		callsPerTurn[c.turn]++
		lastByTurn[c.turn] = c.tasks
		for _, td := range c.tasks {
			if td.Status == task.StatusDone {
				if _, ok := firstDone[td.Text]; !ok {
					firstDone[td.Text] = c.turn + 1 // 1-based completion turn
				}
			}
		}
	}

	statusHistory := make([]statusCounts, turns)
	maxPending := 0
	for turn := 0; turn < turns; turn++ {
		sc := statusCounts{Turn: turn}
		for _, td := range lastByTurn[turn] {
			switch td.Status {
			case task.StatusDone:
				sc.Done++
			case task.StatusInProgress:
				sc.InProgress++
			default:
				sc.Pending++
			}
		}
		statusHistory[turn] = sc
		if sc.Pending > maxPending {
			maxPending = sc.Pending
		}
	}

	totalTasks := 0
	if len(lastByTurn) > 0 && len(lastByTurn[0]) > 0 {
		totalTasks = len(lastByTurn[0])
	}

	doneSum, doneCount := 0, 0
	for _, turn := range firstDone {
		doneSum += turn
		doneCount++
	}
	avg := 0.0
	if doneCount > 0 {
		avg = float64(doneSum) / float64(doneCount)
	}

	var unfinished []string
	if len(lastByTurn[turns-1]) > 0 {
		for _, td := range lastByTurn[turns-1] {
			if td.Status != task.StatusDone {
				unfinished = append(unfinished, td.Text)
			}
		}
	}

	// Build per-task timelines using the task order from the first observed turn.
	timelines := map[string][]task.Status{}
	if len(lastByTurn) > 0 && len(lastByTurn[0]) > 0 {
		for _, td := range lastByTurn[0] {
			timelines[td.Text] = make([]task.Status, turns)
		}
		for turn := 0; turn < turns; turn++ {
			seen := map[string]bool{}
			for _, td := range lastByTurn[turn] {
				if _, ok := timelines[td.Text]; ok {
					timelines[td.Text][turn] = td.Status
					seen[td.Text] = true
				}
			}
			for text := range timelines {
				if !seen[text] {
					timelines[text][turn] = "-"
				}
			}
		}
	}

	return taskMetrics{
		TotalTurns:        turns,
		TotalCalls:        len(calls),
		CallsPerTurn:      callsPerTurn,
		StatusHistory:     statusHistory,
		TotalTasks:        totalTasks,
		DoneTasks:         doneCount,
		AvgCompletionTurn: avg,
		MaxPendingStreak:  maxPending,
		UnfinishedTasks:   unfinished,
		TaskTimelines:     timelines,
	}
}

func renderTaskMetricsMarkdown(m taskMetrics) string {
	var b strings.Builder
	b.WriteString("# Tasks Tool 调用频率与长程任务分指标\n\n")
	b.WriteString(fmt.Sprintf("- 总轮数: %d\n", m.TotalTurns))
	b.WriteString(fmt.Sprintf("- `todo_write` 总调用次数: %d\n", m.TotalCalls))
	b.WriteString(fmt.Sprintf("- 跟踪任务数: %d\n", m.TotalTasks))
	b.WriteString("\n")

	// Invocation frequency
	b.WriteString("## 一、调用频率（按轮次）\n\n")
	b.WriteString("| 轮次 | 调用次数 | 频率条 |\n")
	b.WriteString("|------|----------|--------|\n")
	maxCalls := 0
	for _, c := range m.CallsPerTurn {
		if c > maxCalls {
			maxCalls = c
		}
	}
	if maxCalls == 0 {
		maxCalls = 1
	}
	for turn, c := range m.CallsPerTurn {
		barLen := 20 * c / maxCalls
		if c > 0 && barLen == 0 {
			barLen = 1
		}
		b.WriteString(fmt.Sprintf("| %d | %d | %s |\n", turn, c, strings.Repeat("█", barLen)))
	}
	b.WriteString("\n")

	// Status distribution
	b.WriteString("## 二、长程任务状态分布（按轮次）\n\n")
	b.WriteString("| 轮次 | 待处理 | 进行中 | 已完成 | 状态条 |\n")
	b.WriteString("|------|--------|--------|--------|--------|\n")
	for _, sc := range m.StatusHistory {
		bar := fmt.Sprintf("%s%s%s",
			strings.Repeat("D", sc.Done),
			strings.Repeat("I", sc.InProgress),
			strings.Repeat("P", sc.Pending),
		)
		b.WriteString(fmt.Sprintf("| %d | %d | %d | %d | `%s` |\n",
			sc.Turn, sc.Pending, sc.InProgress, sc.Done, bar))
	}
	b.WriteString("\n")

	// Long-range metrics
	b.WriteString("## 三、长程任务分指标\n\n")
	b.WriteString(fmt.Sprintf("- 已完成任务: %d / %d (%.0f%%)\n", m.DoneTasks, m.TotalTasks, 100*float64(m.DoneTasks)/float64(m.TotalTasks)))
	b.WriteString(fmt.Sprintf("- 平均完成轮次: %.2f\n", m.AvgCompletionTurn))
	b.WriteString(fmt.Sprintf("- 单轮最大待处理数: %d\n", m.MaxPendingStreak))
	if len(m.UnfinishedTasks) == 0 {
		b.WriteString("- 未完成长程任务: 无\n")
	} else {
		b.WriteString("- 未完成长程任务:\n")
		for _, text := range m.UnfinishedTasks {
			b.WriteString(fmt.Sprintf("  - %s\n", text))
		}
	}
	b.WriteString("\n")

	// Per-task phase timeline
	b.WriteString("## 四、每个 todo 的阶段划分\n\n")
	b.WriteString("该表展示每个长程任务在每一轮的状态，用于分析 agent 对任务的阶段划分。\n\n")
	b.WriteString("| 任务 |")
	for turn := 0; turn < m.TotalTurns; turn++ {
		b.WriteString(fmt.Sprintf(" 轮次%d |", turn))
	}
	b.WriteString(" 阶段序列 | 完成轮次 |\n")
	b.WriteString("|------|")
	for i := 0; i < m.TotalTurns; i++ {
		b.WriteString("--------|")
	}
	b.WriteString("----------|----------|\n")

	var texts []string
	for text := range m.TaskTimelines {
		texts = append(texts, text)
	}
	sort.Strings(texts)

	for _, text := range texts {
		timeline := m.TaskTimelines[text]
		b.WriteString(fmt.Sprintf("| %s |", text))
		doneTurn := "-"
		for turn, st := range timeline {
			b.WriteString(fmt.Sprintf(" %s |", statusSymbol(st)))
			if st == task.StatusDone && doneTurn == "-" {
				doneTurn = fmt.Sprintf("%d", turn+1)
			}
		}
		b.WriteString(fmt.Sprintf(" %s | %s |\n", compressedSequence(timeline), doneTurn))
	}
	b.WriteString("\n")
	b.WriteString("图例: `P` = pending, `I` = in_progress, `D` = done, `-` = 未出现。\n\n")

	return b.String()
}

func statusSymbol(st task.Status) string {
	switch st {
	case task.StatusPending:
		return "P"
	case task.StatusInProgress:
		return "I"
	case task.StatusDone:
		return "D"
	default:
		return "-"
	}
}

// compressedSequence returns a human-readable status transition string like
// "P -> I -> D", collapsing consecutive identical states.
func compressedSequence(timeline []task.Status) string {
	if len(timeline) == 0 {
		return ""
	}
	var parts []string
	last := timeline[0]
	parts = append(parts, statusSymbol(last))
	for i := 1; i < len(timeline); i++ {
		if timeline[i] != last {
			parts = append(parts, statusSymbol(timeline[i]))
			last = timeline[i]
		}
	}
	return strings.Join(parts, " -> ")
}
