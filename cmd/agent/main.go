// Command agent — AI-agent runtime (Phase 2 модуля agents).
//
// Отдельный entrypoint в одном модуле с telegram-ботом (cmd/bot). cmd/bot
// остаётся живым до переключения деплоя на cmd/agent.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/botjoker/sambacrm-business-tg/internal/agentstore"
	"github.com/botjoker/sambacrm-business-tg/internal/api"
	"github.com/botjoker/sambacrm-business-tg/internal/llm"
	"github.com/botjoker/sambacrm-business-tg/internal/pii"
	"github.com/botjoker/sambacrm-business-tg/internal/runtime"
	"github.com/botjoker/sambacrm-business-tg/internal/storage"
	"github.com/botjoker/sambacrm-business-tg/internal/tools"
	"github.com/botjoker/sambacrm-business-tg/pkg/utils"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		slog.Error("DATABASE_URL is required")
		os.Exit(1)
	}
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		slog.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	queries := storage.New(pool)
	rt := runtime.New(pool, queries)

	// Redis (для SSE). Необязателен — без него SSE-эндпоинты вернут 503.
	rdb, rerr := utils.NewRedisClient()
	if rerr != nil {
		slog.Warn("redis unavailable, SSE disabled", "err", rerr)
	}

	backendURL := envOr("BACKEND_URL", "http://localhost:3005")
	internalSecret := os.Getenv("INTERNAL_JWT_SECRET")
	jwtFactory := func() (string, error) { return api.IssueInternalToken(internalSecret, time.Hour) }

	// Адаптеры рантайма (sqlc-backed).
	recorder := agentstore.NewRecorder(queries)
	memory := agentstore.NewMemory(queries)
	intake := agentstore.NewIntake(queries)
	takeover := agentstore.NewTakeover(queries)
	resolver := agentstore.NewConvResolver(queries)
	remote := tools.NewRemoteExecutor(backendURL, jwtFactory)
	toolReg := agentstore.NewToolRegistry(queries, remote)
	billing := runtime.NewBillingTracker(backendURL, jwtFactory)
	piiClient := pii.New(os.Getenv("PII_SVC_URL"))
	opProxy := runtime.NewOperatorProxy(resolver, recorder)

	deps := []runtime.AgentOption{
		runtime.WithRecorder(recorder),
		runtime.WithMemory(memory),
		runtime.WithIntake(intake),
		runtime.WithTakeover(takeover),
		runtime.WithToolRegistry(toolReg),
		runtime.WithBilling(billing),
		runtime.WithPII(piiClient),
	}

	sink := api.NewSSEHub(rdb)
	providerFactory := func(ctx context.Context, agentID uuid.UUID) (llm.LLMProvider, runtime.AgentConfig, error) {
		cfg, providerName, model, err := agentstore.LoadAgentConfig(ctx, queries, agentID)
		if err != nil {
			return nil, runtime.AgentConfig{}, err
		}
		prov, err := buildProvider(providerName, model)
		return prov, cfg, err
	}
	ingest := makeIngestTrigger(envOr("RAG_URL", "http://sambacrm-agent-rag:8000"), jwtFactory)

	engine := agentstore.NewEngine(queries, sink, providerFactory, ingest, opProxy, deps...)

	srv := api.NewServer(rt, pool, queries)
	srv.SetRedis(rdb)
	srv.SetEngine(engine)

	addr := envOr("AGENT_HTTP_ADDR", ":8080")
	go srv.Start(addr)
	slog.Info("agent service started", "addr", addr)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down")
	srv.Shutdown(context.Background())
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// buildProvider строит LLM-провайдера по имени. MVP: ключи из ENV (платформенные).
// TODO: per-tenant расшифровка credentials через AGENT_SECRETS_KEY (см. HANDOFF).
func buildProvider(name, model string) (llm.LLMProvider, error) {
	creds := llm.Credentials{Key3: model}
	switch name {
	case "yandex_gpt":
		creds.Key1 = os.Getenv("YANDEX_API_KEY")
		creds.Key2 = os.Getenv("YANDEX_FOLDER_ID")
	case "gigachat":
		creds.Key2 = os.Getenv("GIGACHAT_AUTH_KEY")
		creds.Extra = map[string]string{"scope": envOr("GIGACHAT_SCOPE", "GIGACHAT_API_PERS")}
	case "openai":
		creds.Key1 = os.Getenv("OPENAI_API_KEY")
	case "anthropic":
		creds.Key1 = os.Getenv("ANTHROPIC_API_KEY")
	case "deepseek":
		creds.Key1 = os.Getenv("DEEPSEEK_API_KEY")
	case "openrouter":
		creds.Key1 = os.Getenv("OPENROUTER_API_KEY")
	}
	policy := llm.ProfilePolicy{AllowedLLMProviders: []string{name}}
	return llm.NewProvider(name, creds, policy)
}

// makeIngestTrigger возвращает функцию-триггер индексации (POST в rag-svc /v1/ingest).
func makeIngestTrigger(ragURL string, jwtFactory func() (string, error)) agentstore.IngestTrigger {
	client := &http.Client{Timeout: 10 * time.Second}
	return func(ctx context.Context, sourceID, profileID uuid.UUID) error {
		body, _ := json.Marshal(map[string]string{
			"source_id":  sourceID.String(),
			"profile_id": profileID.String(),
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, ragURL+"/v1/ingest", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if token, err := jwtFactory(); err == nil {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		return nil
	}
}
