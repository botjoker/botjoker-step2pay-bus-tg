package runtime

import (
	"log/slog"

	"github.com/botjoker/sambacrm-business-tg/internal/llm"
)

// AgentOption — функциональная опция для инъекции зависимости в NewAgent.
type AgentOption func(*Agent)

func WithToolRegistry(t ToolRegistry) AgentOption  { return func(a *Agent) { a.tools = t } }
func WithRAG(r RAGClient) AgentOption              { return func(a *Agent) { a.rag = r } }
func WithPII(p PIIClient) AgentOption              { return func(a *Agent) { a.pii = p } }
func WithIntake(i IntakeStore) AgentOption         { return func(a *Agent) { a.intake = i } }
func WithBilling(b BillingTracker) AgentOption     { return func(a *Agent) { a.billing = b } }
func WithMemory(m Memory) AgentOption              { return func(a *Agent) { a.memory = m } }
func WithTakeover(t TakeoverGate) AgentOption      { return func(a *Agent) { a.takeover = t } }
func WithRecorder(r MessageRecorder) AgentOption   { return func(a *Agent) { a.recorder = r } }
func WithFewShot(f FewShotStore) AgentOption       { return func(a *Agent) { a.fewShot = f } }
func WithLogger(l *slog.Logger) AgentOption        { return func(a *Agent) { a.logger = l } }

// NewAgent создаёт агента с дефолтными no-op зависимостями. Реальные адаптеры
// подключаются опциями (см. шаги 038/039/03A–03E/03D).
func NewAgent(cfg AgentConfig, provider llm.LLMProvider, opts ...AgentOption) *Agent {
	a := &Agent{
		cfg:      cfg,
		provider: provider,
		tools:    noopToolRegistry{},
		rag:      noopRAG{},
		pii:      noopPII{},
		intake:   noopIntake{},
		billing:  noopBilling{},
		memory:   noopMemory{},
		takeover: noopTakeover{},
		recorder: noopRecorder{},
		fewShot:  noopFewShot{},
		logger:   slog.Default(),
	}
	for _, opt := range opts {
		opt(a)
	}
	if a.logger == nil {
		a.logger = slog.Default()
	}
	return a
}
