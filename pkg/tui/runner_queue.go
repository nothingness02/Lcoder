package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/models"
)

// promptRequest carries a user prompt into the runner queue.
type promptRequest struct {
	text string
	msg  models.AgentMessage
}

// steerRequest carries a steering message into the runner queue.
type steerRequest struct {
	msg models.AgentMessage
}

// runnerQueue serializes calls to the agent so the UI never blocks the bubbletea
// update loop. It runs in a dedicated goroutine and delivers completion messages
// (AgentDoneMsg) on a channel that the model drains via a tea.Cmd.
type runnerQueue struct {
	agent   AgentRunner
	session SessionWriter
	prompts chan promptRequest
	steers  chan steerRequest
	results chan tea.Msg
}

// newRunnerQueue creates a queue for the given agent and session.
func newRunnerQueue(agent AgentRunner, session SessionWriter) *runnerQueue {
	return &runnerQueue{
		agent:   agent,
		session: session,
		prompts: make(chan promptRequest, 8),
		steers:  make(chan steerRequest, 8),
		results: make(chan tea.Msg, 8),
	}
}

// Start begins processing requests in a background goroutine. The goroutine exits
// when ctx is cancelled.
func (q *runnerQueue) Start(ctx context.Context) {
	go q.run(ctx)
}

func (q *runnerQueue) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case p := <-q.prompts:
			if err := q.session.Append(p.msg); err != nil {
				q.sendResult(ctx, AgentDoneMsg{Err: err})
				continue
			}
			err := q.agent.Prompt(ctx, p.msg)
			q.sendResult(ctx, AgentDoneMsg{Err: err})
		case s := <-q.steers:
			q.agent.Steer(s.msg)
		}
	}
}

func (q *runnerQueue) sendResult(ctx context.Context, msg tea.Msg) {
	select {
	case q.results <- msg:
	case <-ctx.Done():
	}
}

// SubmitPrompt enqueues a user prompt. The message is appended to the session
// and submitted to the agent by the worker.
func (q *runnerQueue) SubmitPrompt(text string) {
	q.prompts <- promptRequest{text: text, msg: models.UserMessage(text)}
}

// SubmitSteer enqueues a steering message for the currently running agent.
func (q *runnerQueue) SubmitSteer(msg models.AgentMessage) {
	q.steers <- steerRequest{msg: msg}
}

// Results returns the channel that receives completion messages from the worker.
func (q *runnerQueue) Results() <-chan tea.Msg {
	return q.results
}

// SetAgent updates the agent target. Used after a mode switch, which replaces
// the active agent.
func (q *runnerQueue) SetAgent(agent AgentRunner) {
	q.agent = agent
}

// SetSession updates the session target. Used after switching or creating a
// session so subsequent prompts append to the correct conversation.
func (q *runnerQueue) SetSession(session SessionWriter) {
	q.session = session
}
