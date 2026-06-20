-- ============================================================
-- ============  AGENTS MODULE (Phase 2 runtime)  =============
-- ============================================================
-- read-only для runtime; CRUD остаётся в backend (Rust).
-- ВНИМАНИЕ: agents.proactive_quiet_hours_local (TSTZRANGE) НЕ селектим —
-- pgx/sqlc не умеет этот тип; proactive отложен в V1.1.

-- name: GetCredential :one
-- Кредал тенанта (ключи провайдера, key1/2/3 зашифрованы AGENT_SECRETS_KEY).
SELECT id, profile_id, credential_type, key1, key2, key3, metadata, is_active
FROM agent_credentials WHERE id = $1;

-- name: GetAgent :one
SELECT id, profile_id, slug, name, description, avatar_media_id,
       persona, greeting_message, fallback_message, safety_disclaimer,
       llm_provider, llm_model, llm_credentials_id, llm_temperature,
       llm_max_tokens, llm_max_iterations,
       embedding_provider, embedding_model, embedding_credentials_id, embedding_dim,
       rag_enabled, rag_top_k, rag_min_score,
       default_language, auto_detect_language, allowed_languages,
       vision_enabled, vision_model,
       takeover_enabled, takeover_notify_channel, takeover_notify_target,
       proactive_enabled,
       brand_color, brand_logo_media_id, brand_powered_by_hidden,
       plan_tier, share_facts_across_channels,
       is_active, is_deleted,
       created_at, updated_at, created_by, updated_by
FROM agents
WHERE id = $1 AND is_deleted = false;

-- name: GetAgentBySlug :one
SELECT id, profile_id, slug, name, description, avatar_media_id,
       persona, greeting_message, fallback_message, safety_disclaimer,
       llm_provider, llm_model, llm_credentials_id, llm_temperature,
       llm_max_tokens, llm_max_iterations,
       embedding_provider, embedding_model, embedding_credentials_id, embedding_dim,
       rag_enabled, rag_top_k, rag_min_score,
       default_language, auto_detect_language, allowed_languages,
       vision_enabled, vision_model,
       takeover_enabled, takeover_notify_channel, takeover_notify_target,
       proactive_enabled,
       brand_color, brand_logo_media_id, brand_powered_by_hidden,
       plan_tier, share_facts_across_channels,
       is_active, is_deleted,
       created_at, updated_at, created_by, updated_by
FROM agents
WHERE profile_id = $1 AND slug = $2 AND is_deleted = false;

-- ============================================================
-- AGENT_TOOLS
-- ============================================================

-- name: ListEnabledTools :many
SELECT * FROM agent_tools
WHERE agent_id = $1 AND is_enabled = true;

-- name: ListAgentSellableDocuments :many
-- Платные документы, доступные агенту: привязанные к нему ИЛИ универсальные
-- агентские шаблоны тенанта (entity_type='agent', без конкретного agent_id).
-- Используется для инъекции перечня в системный промпт (AC-3).
SELECT id, name, doc_type, variables, price_amount, currency, disclaimer
FROM document_templates
WHERE profile_id = $1
  AND is_active = true
  AND is_deleted = false
  AND (agent_id = $2 OR (entity_type = 'agent' AND agent_id IS NULL))
ORDER BY name;

-- ============================================================
-- AGENT_CHANNELS
-- ============================================================

-- name: GetChannel :one
SELECT * FROM agent_channels
WHERE id = $1 AND is_deleted = false;

-- name: ListActiveChannelsByType :many
SELECT * FROM agent_channels
WHERE channel_type = $1 AND is_active = true AND is_deleted = false;

-- name: GetChannelByWebSlug :one
SELECT * FROM agent_channels
WHERE profile_id = $1 AND web_slug = $2 AND is_active = true AND is_deleted = false;

-- name: GetWebChannelBySlug :one
-- Публичный резолв виджета по slug (web_slug глобально адресуем).
SELECT * FROM agent_channels
WHERE web_slug = $1 AND channel_type = 'web' AND is_active = true AND is_deleted = false
LIMIT 1;

-- ============================================================
-- AGENT_CONVERSATIONS
-- ============================================================
-- get-or-create реализован в Go двумя запросами (надёжнее для sqlc, чем
-- CTE с UNION ALL + INSERT...RETURNING из исходного плана).

-- name: GetAgentConversation :one
SELECT * FROM agent_conversations WHERE id = $1;

-- name: GetAgentConversationByExternal :one
SELECT * FROM agent_conversations
WHERE agent_id = $1 AND channel_id = $2 AND external_user_id = $3 AND is_active = true
LIMIT 1;

-- name: CountConversationMessages :one
SELECT COUNT(*) FROM agent_messages WHERE conversation_id = $1;

-- name: CreateAgentConversation :one
INSERT INTO agent_conversations
  (profile_id, agent_id, channel_id, external_user_id, external_chat_id, customer_id)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: UpdateConversationContext :exec
UPDATE agent_conversations
SET context = context || $2::jsonb, last_message_at = NOW()
WHERE id = $1;

-- name: UpdateConversationSummary :exec
UPDATE agent_conversations
SET summary = $2, last_message_at = NOW()
WHERE id = $1;

-- ============================================================
-- AGENT_MESSAGES
-- ============================================================

-- name: InsertMessage :one
INSERT INTO agent_messages (
  profile_id, conversation_id, role, content, content_original,
  tool_calls, tool_call_id, tool_result,
  retrieved_chunks, citations, attachments, has_image,
  detected_language, response_language,
  operator_account_id,
  redaction_applied, redaction_log,
  tokens_in, tokens_out, cost_usd, latency_ms,
  llm_model, llm_provider
) VALUES (
  $1,$2,$3,$4,$5, $6,$7,$8, $9,$10,$11,$12, $13,$14, $15,
  $16,$17, $18,$19,$20,$21, $22,$23
)
RETURNING *;

-- name: NotifyConvEvent :exec
-- Live-событие для админского SSE (канал agent_conv_event:{profile_id}).
SELECT pg_notify(sqlc.arg(channel)::text, sqlc.arg(payload)::text);

-- name: GetAgentGreeting :one
SELECT greeting_message FROM agents WHERE id = $1;

-- name: InsertGreetingMessage :exec
-- Приветствие агента как первое assistant-сообщение нового диалога.
INSERT INTO agent_messages (profile_id, conversation_id, role, content)
VALUES ($1, $2, 'assistant', $3);

-- name: ListMessagesByConversation :many
SELECT * FROM agent_messages
WHERE conversation_id = $1
ORDER BY created_at ASC
LIMIT $2 OFFSET $3;

-- name: ChatHistory :many
-- История для веб-виджета: только видимые реплики (user/assistant/operator),
-- без tool/system и пустых. По возрастанию времени.
SELECT role, content, created_at
FROM agent_messages
WHERE conversation_id = $1
  AND role IN ('user', 'assistant', 'operator')
  AND content IS NOT NULL
  AND content <> ''
ORDER BY created_at ASC
LIMIT $2;

-- name: LastMessages :many
SELECT * FROM agent_messages
WHERE conversation_id = $1
ORDER BY created_at DESC
LIMIT $2;

-- ============================================================
-- AGENT_INTAKE_FIELDS + FACTS
-- ============================================================

-- name: ListIntakeFields :many
SELECT * FROM agent_intake_fields
WHERE agent_id = $1 AND is_active = true AND is_deleted = false
ORDER BY ask_priority;

-- name: ListConversationFacts :many
SELECT * FROM agent_conversation_facts
WHERE conversation_id = $1 AND superseded_by IS NULL;

-- name: InsertConversationFact :one
INSERT INTO agent_conversation_facts
  (profile_id, conversation_id, intake_field_id, field_key, field_value,
   confidence, source_message_id, source_excerpt)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
RETURNING *;

-- name: SupersedeFact :exec
UPDATE agent_conversation_facts
SET superseded_by = $2
WHERE id = $1;

-- name: DeactivateActiveFact :exec
-- Само-вытеснение активного факта по ключу (освобождает partial-unique индекс
-- перед вставкой нового значения). superseded_by = id → строка перестаёт быть активной.
UPDATE agent_conversation_facts
SET superseded_by = id
WHERE conversation_id = $1 AND field_key = $2 AND superseded_by IS NULL;

-- name: VerifyFact :exec
UPDATE agent_conversation_facts
SET is_verified = true, updated_at = NOW()
WHERE conversation_id = $1 AND field_key = $2 AND superseded_by IS NULL;

-- ============================================================
-- AGENT_TAKEOVERS
-- ============================================================

-- name: GetActiveTakeover :one
SELECT * FROM agent_takeovers
WHERE conversation_id = $1 AND ended_at IS NULL
LIMIT 1;

-- ============================================================
-- OUTREACH / CITATIONS (local tools 084)
-- ============================================================

-- name: AppendMessageCitation :exec
UPDATE agent_messages
SET citations = COALESCE(citations, '[]'::jsonb) || $2::jsonb
WHERE id = $1;

-- name: EndTakeover :exec
UPDATE agent_takeovers
SET ended_at = NOW()
WHERE id = $1;

-- ============================================================
-- KNOWLEDGE (RAG sidecar пишет chunks напрямую через asyncpg в Python;
-- runtime только читает source + обновляет статус)
-- ============================================================

-- name: GetKnowledgeSource :one
SELECT * FROM agent_knowledge_sources WHERE id = $1;

-- name: UpdateSourceStatus :exec
UPDATE agent_knowledge_sources
SET status = $2, error_message = $3, indexed_at = COALESCE($4, indexed_at),
    chunks_count = COALESCE($5, chunks_count),
    updated_at = NOW()
WHERE id = $1;

-- ============================================================
-- BILLING
-- ============================================================

-- name: GetCurrentUsage :one
SELECT * FROM agent_billing_usage
WHERE profile_id = $1 AND period_start = date_trunc('month', NOW())::date
LIMIT 1;

-- ============================================================
-- INSIGHTS (post-conversation tagging, 092/093)
-- ============================================================

-- name: ListDueForInsights :many
-- Диалоги без активности > 30 мин и ещё без insights.
SELECT c.id, c.profile_id
FROM agent_conversations c
LEFT JOIN agent_conversation_insights i ON i.conversation_id = c.id
WHERE i.id IS NULL
  AND c.last_message_at < NOW() - INTERVAL '30 minutes'
ORDER BY c.last_message_at ASC
LIMIT $1;

-- name: UpsertInsights :exec
INSERT INTO agent_conversation_insights
  (profile_id, conversation_id, tags, sentiment, sentiment_confidence,
   primary_intent, secondary_intents, topics, short_summary, long_summary,
   contact_extracted, next_step_suggestion, converted, conversion_tool, llm_model)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
ON CONFLICT (conversation_id) DO UPDATE SET
  tags = EXCLUDED.tags,
  sentiment = EXCLUDED.sentiment,
  sentiment_confidence = EXCLUDED.sentiment_confidence,
  primary_intent = EXCLUDED.primary_intent,
  secondary_intents = EXCLUDED.secondary_intents,
  topics = EXCLUDED.topics,
  short_summary = EXCLUDED.short_summary,
  long_summary = EXCLUDED.long_summary,
  contact_extracted = EXCLUDED.contact_extracted,
  next_step_suggestion = EXCLUDED.next_step_suggestion,
  converted = EXCLUDED.converted,
  conversion_tool = EXCLUDED.conversion_tool,
  llm_model = EXCLUDED.llm_model,
  generated_at = NOW();

-- name: ListInsightsForDigest :many
-- Insights тенанта за период (для weekly digest, 093).
SELECT tags, sentiment, primary_intent, topics, converted, short_summary
FROM agent_conversation_insights
WHERE profile_id = sqlc.arg(profile_id)
  AND generated_at >= sqlc.arg(period_from)
  AND generated_at < sqlc.arg(period_to);

-- name: ListAgentProfileIds :many
-- Тенанты с хотя бы одним агентом (для планирования дайджестов).
SELECT DISTINCT profile_id FROM agents WHERE is_deleted = false;

-- name: InsertDigest :exec
INSERT INTO agent_insight_digests
  (profile_id, agent_id, period_start, period_end, metrics, insights)
VALUES ($1, $2, $3, $4, $5, $6);

-- ============================================================
-- FEW-SHOT (101 — promoted feedback в system prompt)
-- ============================================================

-- name: ListPromotedFeedback :many
SELECT message_id, correction
FROM agent_feedback
WHERE agent_id = $1 AND promoted_to_few_shot = true
ORDER BY promoted_at DESC NULLS LAST
LIMIT $2;

-- name: GetMessageBrief :one
SELECT content, conversation_id, created_at
FROM agent_messages WHERE id = $1;

-- name: GetPrecedingUserMessage :one
SELECT content FROM agent_messages
WHERE conversation_id = sqlc.arg(conversation_id)
  AND role = 'user'
  AND created_at < sqlc.arg(before)
ORDER BY created_at DESC
LIMIT 1;

-- ============================================================
-- LEADS AUTO-RULE (ЭТАП D2) — авто-создание лида из «горячего» диалога
-- ============================================================

-- name: GetAgentAutoCreateLead :one
SELECT auto_create_lead FROM agents WHERE id = $1;

-- name: GetLeadStartStage :one
-- Стартовая стадия тенанта (минимальный sort_order, не won/lost).
SELECT id FROM lead_stages
WHERE profile_id = $1 AND is_deleted = false AND is_won = false AND is_lost = false
ORDER BY sort_order LIMIT 1;

-- name: FindOpenLeadByPhone :one
-- Открытый (не won/lost) лид с таким телефоном — для мягкого дедупа.
SELECT l.id FROM leads l
WHERE l.profile_id = $1 AND l.is_deleted = false AND l.contact_phone = $2
  AND NOT EXISTS (
    SELECT 1 FROM lead_stages s
    WHERE s.id = l.stage_id AND (s.is_won = true OR s.is_lost = true)
  )
ORDER BY l.created_at DESC LIMIT 1;

-- name: InsertLeadAuto :one
INSERT INTO leads
  (profile_id, stage_id, title, contact_name, contact_phone, contact_email,
   source, source_agent_id, source_conversation_id, data, summary, sentiment,
   primary_intent, sort_order)
VALUES ($1, $2, $3, $4, $5, $6, 'agent', $7, $8, $9, $10, $11, $12,
   COALESCE((SELECT MAX(sort_order) + 1 FROM leads
             WHERE profile_id = $1 AND stage_id = $2 AND is_deleted = false), 0))
RETURNING id;

-- name: SetConversationLead :exec
UPDATE agent_conversations SET lead_id = $2 WHERE id = $1;
