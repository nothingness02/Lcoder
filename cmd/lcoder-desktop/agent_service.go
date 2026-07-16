package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/lcoder/lcoder/pkg/desktop"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
)

// AgentService exposes agent operations to the Wails frontend.
type AgentService struct {
	runtime *desktop.Runtime
	mu      sync.Mutex
	running bool
}

func NewAgentService(runtime *desktop.Runtime) *AgentService {
	return &AgentService{runtime: runtime}
}

func (s *AgentService) Prompt(text string) error {
	text = trimInput(text)
	if text == "" {
		return fmt.Errorf("empty prompt")
	}

	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("agent is already running")
	}
	s.running = true
	s.mu.Unlock()

	msg := models.NewAgentMessage(models.RoleUser, models.TextContent{Text: text})
	if err := s.runtime.Session.Append(msg); err != nil {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		return fmt.Errorf("append message: %w", err)
	}

	// Let the frontend immediately render the user message and trigger persistence.
	ctx := context.Background()
	_ = s.runtime.Bus.Emit(ctx, events.MessageStartEvent{
		Base:    events.Base{Type: events.MessageStart},
		Message: msg,
	})
	_ = s.runtime.Bus.Emit(ctx, events.MessageEndEvent{
		Base:    events.Base{Type: events.MessageEnd},
		Message: msg,
	})

	go func() {
		defer func() {
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
		}()
		if err := s.runtime.Agent.Prompt(ctx, msg); err != nil {
			_ = s.runtime.Bus.Emit(ctx, events.ErrorEvent{
				Base:    events.Base{Type: events.Error},
				Message: err.Error(),
			})
		}
	}()
	return nil
}

func (s *AgentService) Steer(text string) error {
	if text = trimInput(text); text == "" {
		return fmt.Errorf("empty steer")
	}
	msg := models.NewAgentMessage(models.RoleUser, models.TextContent{Text: text})
	s.runtime.Agent.Steer(msg)
	return nil
}

func (s *AgentService) Abort() {
	s.runtime.Agent.Abort()
}

func (s *AgentService) LoadSession(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return fmt.Errorf("cannot switch session while agent is running")
	}

	sess, err := s.runtime.SessionStore.LoadByID(s.runtime.CWD, id)
	if err != nil {
		return fmt.Errorf("load session: %w", err)
	}
	s.runtime.Agent.SetMessages(sess.EffectiveMessages())
	s.runtime.Session = sess
	s.runtime.Agent.SetSessionID(sess.ID)
	_ = s.runtime.Bus.Emit(context.Background(), events.SessionLoadedEvent{
		Base:      events.Base{Type: events.SessionLoaded},
		SessionID: sess.ID,
		Messages:  s.runtime.Agent.AllMessages(),
	})
	return nil
}

func (s *AgentService) NewSession() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return "", fmt.Errorf("cannot switch session while agent is running")
	}

	sess, err := s.runtime.SessionStore.Create(s.runtime.CWD)
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	s.runtime.Agent.SetMessages(nil)
	s.runtime.Session = sess
	s.runtime.Agent.SetSessionID(sess.ID)
	_ = s.runtime.Bus.Emit(context.Background(), events.SessionLoadedEvent{
		Base:      events.Base{Type: events.SessionLoaded},
		SessionID: sess.ID,
		Messages:  nil,
	})
	return sess.ID, nil
}

func (s *AgentService) ListSessions() []desktop.SessionSummary {
	sessions, err := s.runtime.SessionStore.List(s.runtime.CWD)
	if err != nil {
		return nil
	}
	out := make([]desktop.SessionSummary, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, desktop.SessionSummary{
			ID:        sess.ID,
			CreatedAt: sess.CreatedAt,
		})
	}
	return out
}

func (s *AgentService) GetMessages() []desktop.UIMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return desktop.MessagesToUI(s.runtime.Agent.AllMessages())
}

func (s *AgentService) GetConfig() desktop.UIConfig {
	return desktop.UIConfig{
		Provider: s.runtime.Config.Provider,
		Model:    s.runtime.Config.Model,
		Mode:     s.runtime.Agent.Mode(),
		CWD:      s.runtime.CWD,
	}
}

func (s *AgentService) SubmitPermission(id string, allow bool, scope string) error {
	return s.runtime.Permissions.Submit(id, allow, scope)
}

func trimInput(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\n') {
		s = s[:len(s)-1]
	}
	return s
}
