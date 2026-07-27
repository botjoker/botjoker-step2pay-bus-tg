// Command agent — AI-agent runtime (Phase 2 модуля agents).
//
// Отдельный entrypoint в одном модуле с telegram-ботом (cmd/bot). cmd/bot
// остаётся живым до переключения деплоя на cmd/agent.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/botjoker/sambacrm-business-tg/internal/agentstore"
	"github.com/botjoker/sambacrm-business-tg/internal/api"
	"github.com/botjoker/sambacrm-business-tg/internal/llm"
	"github.com/botjoker/sambacrm-business-tg/internal/pii"
	"github.com/botjoker/sambacrm-business-tg/internal/queue"
	"github.com/botjoker/sambacrm-business-tg/internal/runtime"
	"github.com/botjoker/sambacrm-business-tg/internal/storage"
	"github.com/botjoker/sambacrm-business-tg/internal/tools"
	"github.com/botjoker/sambacrm-business-tg/internal/transports/telegram"
	"github.com/botjoker/sambacrm-business-tg/internal/transports/vk"
	"github.com/botjoker/sambacrm-business-tg/pkg/utils"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	tele "gopkg.in/telebot.v3"
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
	opProxy := runtime.NewOperatorProxy(resolver)

	// Insights pipeline (092) + weekly digest (093) + inline fact extractor.
	// Создаётся ДО deps, чтобы передать insightsSvc как FactExtractor в NewAgent.
	// Если провайдер недоступен — extractor остаётся noop, работоспособность сохраняется.
	var insightsSvc *agentstore.InsightsService
	var digestSvc *agentstore.DigestService
	if cheap, err := buildProvider(envOr("INSIGHTS_PROVIDER", "yandex_gpt"), envOr("INSIGHTS_MODEL", "yandexgpt-lite")); err == nil {
		model := envOr("INSIGHTS_MODEL", "yandexgpt-lite")
		insightsSvc = agentstore.NewInsightsService(queries, cheap, model)
		digestSvc = agentstore.NewDigestService(queries, cheap, model, makeNotifier(backendURL, jwtFactory))
	} else {
		slog.Warn("insights disabled (provider build failed)", "err", err)
	}

	deps := []runtime.AgentOption{
		runtime.WithRecorder(recorder),
		runtime.WithMemory(memory),
		runtime.WithIntake(intake),
		runtime.WithTakeover(takeover),
		runtime.WithToolRegistry(toolReg),
		runtime.WithBilling(billing),
		runtime.WithPII(piiClient),
		runtime.WithFewShot(agentstore.NewFewShot(queries)),
		// Retrieval: без этого rag по умолчанию noop и агент не использует базу знаний.
		runtime.WithRAG(newRAGClient(envOr("RAG_URL", "http://sambacrm-agent-rag:8000"), jwtFactory)),
	}
	if insightsSvc != nil {
		// Inline extractor: короткий проход дешёвой моделью после каждого user-message,
		// чтобы вытащить факты, которые основной LLM мог пропустить.
		deps = append(deps, runtime.WithExtractor(insightsSvc))
	}

	sink := api.NewSSEHub(rdb)
	agentSecretsKey := os.Getenv("AGENT_SECRETS_KEY")
	providerFactory := func(ctx context.Context, agentID uuid.UUID) (llm.LLMProvider, runtime.AgentConfig, error) {
		cfg, providerName, model, credID, err := agentstore.LoadAgentConfig(ctx, queries, agentID)
		if err != nil {
			return nil, runtime.AgentConfig{}, err
		}
		// LLM-ключи берутся ТОЛЬКО из credentials тенанта (заданы в админке,
		// шифр AGENT_SECRETS_KEY). Никакого ENV-fallback для агентов.
		if credID == uuid.Nil {
			return nil, cfg, fmt.Errorf("agent %s: LLM credential не задан (укажите в админке)", agentID)
		}
		if agentSecretsKey == "" {
			return nil, cfg, fmt.Errorf("AGENT_SECRETS_KEY не сконфигурирован")
		}
		creds, err := agentstore.LoadProviderCredentials(ctx, queries, credID, agentSecretsKey, model)
		if err != nil {
			return nil, cfg, fmt.Errorf("load tenant credentials: %w", err)
		}
		policy := llm.ProfilePolicy{AllowedLLMProviders: []string{providerName}}
		prov, err := llm.NewProvider(providerName, *creds, policy)
		return prov, cfg, err
	}
	ingest := makeIngestTrigger(envOr("RAG_URL", "http://sambacrm-agent-rag:8000"), jwtFactory)

	engine := agentstore.NewEngine(queries, sink, providerFactory, ingest, opProxy, deps...)
	engine.SetSecretsKey(agentSecretsKey) // ключ для расшифровки токенов каналов (F01)
	if publicURL := strings.TrimRight(envOr("AGENT_PUBLIC_URL", ""), "/"); publicURL != "" {
		engine.SetIntakeFormLink(func(convID uuid.UUID) string {
			token, err := api.IssueIntakeToken(internalSecret, convID.String(), 30*time.Minute)
			if err != nil {
				return ""
			}
			return publicURL + "/chat/intake?token=" + url.QueryEscape(token)
		})
	}

	// Telegram-транспорт: запускаем ботов активных каналов + регистрируем в op-proxy.
	tgManager := telegram.NewManager(engine)
	if chans, err := engine.ListTelegramChannels(ctx); err == nil {
		for _, c := range chans {
			if err := tgManager.Start(c.ChannelID, c.Token); err != nil {
				slog.Warn("telegram channel start failed", "channel", c.ChannelID, "err", err)
				continue
			}
			// Регистрируем webhook в Telegram, если задан публичный URL (F02).
			if pub := envOr("AGENT_PUBLIC_URL", ""); pub != "" {
				if err := tgManager.SetWebhook(c.ChannelID, pub, internalSecret); err != nil {
					slog.Warn("telegram setWebhook failed", "channel", c.ChannelID, "err", err)
				}
			}
		}
		slog.Info("telegram channels started", "count", tgManager.Count())
	} else {
		slog.Warn("list telegram channels failed", "err", err)
	}
	opProxy.Register("telegram", tgManager)

	// VK-транспорт.
	vkManager := vk.NewManager(engine)
	if chans, err := engine.ListVKChannels(ctx); err == nil {
		for _, c := range chans {
			// Confirmation-код берём из БД (per-channel); ENV — только legacy-fallback.
			conf := c.Confirmation
			if conf == "" {
				conf = envOr("VK_CONFIRMATION_"+strconv.FormatInt(c.GroupID, 10), os.Getenv("VK_CONFIRMATION"))
			}
			if err := vkManager.Start(vk.Channel{
				ChannelID:    c.ChannelID,
				AccessToken:  c.AccessToken,
				SecretKey:    c.SecretKey,
				Confirmation: conf,
				GroupID:      c.GroupID,
			}); err != nil {
				slog.Warn("vk channel start failed", "channel", c.ChannelID, "err", err)
			}
		}
		slog.Info("vk channels started", "count", vkManager.Count())
	} else {
		slog.Warn("list vk channels failed", "err", err)
	}
	opProxy.Register("vk", vkManager)

	// Web-виджет: операторские сообщения доставляются через SSE (тот же хаб, что и
	// события агента). Без Redis web-доставка вернёт ошибку (как и сам SSE-чат).
	opProxy.Register("web", api.NewWebSender(sink))

	// Asynq-воркеры для post-processing insights и weekly digest.
	// Сами сервисы уже созданы выше (нужны для inline extractor в NewAgent).
	if insightsSvc != nil && digestSvc != nil {
		startInsights(ctx, insightsSvc, digestSvc, engine)
	}

	srv := api.NewServer(rt, pool, queries)
	srv.SetRedis(rdb)
	srv.SetEngine(engine)
	srv.SetWebhookHandler("telegram", func(ctx context.Context, channelID uuid.UUID, body []byte) error {
		var update tele.Update
		if err := json.Unmarshal(body, &update); err != nil {
			return err
		}
		return tgManager.HandleUpdate(ctx, channelID, &update)
	})
	srv.SetVKWebhookHandler(vkManager.HandleCallback)
	srv.SetChannelReloader(&channelReloader{
		engine:         engine,
		tgManager:      tgManager,
		vkManager:      vkManager,
		agentPublicURL: os.Getenv("AGENT_PUBLIC_URL"),
		internalSecret: os.Getenv("INTERNAL_JWT_SECRET"),
	})

	addr := envOr("AGENT_HTTP_ADDR", ":8080")
	go srv.Start(addr)
	slog.Info("agent service started", "addr", addr)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down")
	srv.Shutdown(context.Background())
}

// channelReloader — реализация api.ChannelReloader. Дёргается backend'ом
// после save/update/delete канала, чтобы админке не приходилось ждать рестарта
// пода (F02.2). Для upsert перечитывает один канал из БД, расшифровывает и
// перезапускает его в соответствующем manager'е; для telegram дополнительно
// обновляет webhook в Telegram API.
type channelReloader struct {
	engine         *agentstore.Engine
	tgManager      *telegram.Manager
	vkManager      *vk.Manager
	agentPublicURL string
	internalSecret string
}

func (r *channelReloader) Reload(ctx context.Context, channelID uuid.UUID, channelType string) error {
	switch channelType {
	case "vk":
		info, err := r.engine.GetVKChannel(ctx, channelID)
		if err != nil {
			return err
		}
		if info == nil {
			r.vkManager.Stop(channelID)
			return nil
		}
		return r.vkManager.Start(vk.Channel{
			ChannelID:    info.ChannelID,
			AccessToken:  info.AccessToken,
			SecretKey:    info.SecretKey,
			Confirmation: info.Confirmation,
			GroupID:      info.GroupID,
		})
	case "telegram":
		info, err := r.engine.GetTelegramChannel(ctx, channelID)
		if err != nil {
			return err
		}
		if info == nil {
			r.tgManager.Stop(channelID)
			return nil
		}
		if err := r.tgManager.Start(info.ChannelID, info.Token); err != nil {
			return err
		}
		if r.agentPublicURL != "" {
			if err := r.tgManager.SetWebhook(info.ChannelID, r.agentPublicURL, r.internalSecret); err != nil {
				slog.Warn("telegram setWebhook failed on reload", "channel", channelID, "err", err)
			}
		}
		return nil
	default:
		// Web/max/avito пока без in-memory-state — reload не нужен.
		return nil
	}
}

func (r *channelReloader) Remove(channelID uuid.UUID, channelType string) {
	switch channelType {
	case "vk":
		r.vkManager.Stop(channelID)
	case "telegram":
		r.tgManager.Stop(channelID)
	}
}

// startInsights запускает Asynq-воркеры (insights + weekly digest) и планировщики.
func startInsights(ctx context.Context, insights *agentstore.InsightsService, digest *agentstore.DigestService, engine *agentstore.Engine) {
	client, err := queue.NewAsynqClient()
	if err != nil {
		slog.Warn("insights: asynq client unavailable", "err", err)
		return
	}

	// Пост-анализ сразу после хода (с дебаунсом), а не только по 30-мин расписанию.
	// TaskID=convID + ProcessIn схлопывают серию сообщений в один прогон на окно.
	engine.SetPostTurn(func(_ context.Context, convID uuid.UUID) {
		task, terr := queue.NewInsightsTask(convID)
		if terr != nil {
			return
		}
		_, eerr := client.Enqueue(task,
			asynq.TaskID("insights:"+convID.String()),
			asynq.ProcessIn(90*time.Second),
		)
		if eerr != nil && !errors.Is(eerr, asynq.ErrTaskIDConflict) && !errors.Is(eerr, asynq.ErrDuplicateTask) {
			slog.Warn("insights: post-turn enqueue failed", "conversation", convID, "err", eerr)
		}
	})
	srv, err := queue.NewAsynqServer()
	if err != nil {
		slog.Warn("insights: asynq server unavailable", "err", err)
		return
	}

	mux := asynq.NewServeMux()
	mux.HandleFunc(queue.TypeConversationInsights, func(ctx context.Context, t *asynq.Task) error {
		p, err := queue.ParseInsightsPayload(t)
		if err != nil {
			return err
		}
		return insights.Process(ctx, p.ConversationID)
	})
	mux.HandleFunc(queue.TypeWeeklyDigest, func(ctx context.Context, t *asynq.Task) error {
		p, err := queue.ParseWeeklyDigestPayload(t)
		if err != nil {
			return err
		}
		return digest.GenerateAndStore(ctx, p.ProfileID, nowUTC())
	})
	if err := srv.Start(mux); err != nil {
		slog.Warn("insights: asynq start failed", "err", err)
		return
	}

	// Планировщик insights: раз в 5 минут — «остывшие» диалоги.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				ids, err := insights.DueConversations(ctx, 50)
				if err != nil {
					slog.Warn("insights: due query failed", "err", err)
					continue
				}
				for _, id := range ids {
					if task, err := queue.NewInsightsTask(id); err == nil {
						_, _ = client.Enqueue(task)
					}
				}
			}
		}
	}()

	// Планировщик дайджестов: проверка раз в час, запуск по понедельникам ~08:00 UTC.
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		lastRun := ""
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				now := nowUTC()
				stamp := now.Format("2006-01-02")
				if now.Weekday() == time.Monday && now.Hour() == 8 && lastRun != stamp {
					lastRun = stamp
					pids, err := digest.ProfileIDs(ctx)
					if err != nil {
						slog.Warn("digest: profiles query failed", "err", err)
						continue
					}
					for _, pid := range pids {
						if task, err := queue.NewWeeklyDigestTask(pid); err == nil {
							_, _ = client.Enqueue(task)
						}
					}
					slog.Info("weekly digests enqueued", "tenants", len(pids))
				}
			}
		}
	}()

	slog.Info("insights + digest pipeline started")
}

func nowUTC() time.Time { return time.Now().UTC() }

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
// makeNotifier создаёт NotifyFunc, отправляющую письмо владельцу тенанта через
// backend (POST /internal/agents/notify, internal-JWT). Получателя (email владельца)
// определяет backend по profile_id.
func makeNotifier(backendURL string, jwtFactory func() (string, error)) agentstore.NotifyFunc {
	client := &http.Client{Timeout: 10 * time.Second}
	return func(ctx context.Context, profileID uuid.UUID, subject, html string) error {
		body, _ := json.Marshal(map[string]any{
			"profile_id": profileID.String(),
			"subject":    subject,
			"html":       html,
			"kind":       "weekly_digest",
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, backendURL+"/internal/agents/notify", bytes.NewReader(body))
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
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("notify backend status %d", resp.StatusCode)
		}
		return nil
	}
}

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
