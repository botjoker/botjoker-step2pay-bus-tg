-- name: GetAllActiveBots :many
SELECT 
    id, profile_id, bot_token, bot_username, bot_name, is_active,
    welcome_message, ai_enabled, ai_provider, ai_model, 
    ai_system_prompt, ai_temperature, ai_max_tokens,
    created_at, updated_at
FROM telegram_bots
WHERE is_active = true;

-- name: GetWorkflow :one
SELECT id, profile_id, bot_id, workflow_name, workflow_key, description,
       trigger_type, trigger_config, is_active,
       created_at, updated_at, created_by
FROM telegram_workflows
WHERE id = $1;

-- name: GetActiveWorkflowsByBot :many
SELECT id, profile_id, bot_id, workflow_name, workflow_key, description,
       trigger_type, trigger_config, is_active,
       created_at, updated_at, created_by
FROM telegram_workflows
WHERE bot_id = $1 AND is_active = true;

-- name: GetActiveWorkflowsByProfile :many
SELECT id, profile_id, bot_id, workflow_name, workflow_key, description,
       trigger_type, trigger_config, is_active,
       created_at, updated_at, created_by
FROM telegram_workflows
WHERE profile_id = $1 AND is_active = true;

-- name: GetWorkflowNodes :many
SELECT id, workflow_id, node_key, node_type, node_label,
       position_x, position_y, config, credentials_id, created_at
FROM telegram_workflow_nodes
WHERE workflow_id = $1
ORDER BY position_y, position_x;

-- name: GetWorkflowEdges :many
SELECT id, workflow_id, source_node_id, target_node_id,
       condition_field, condition_operator, condition_value, created_at
FROM telegram_workflow_edges
WHERE workflow_id = $1;

-- name: CreateExecution :one
INSERT INTO telegram_executions (
    id, profile_id, workflow_id, telegram_user_id, chat_id,
    status, input_data, started_at
) VALUES (
    gen_random_uuid(), $1, $2, $3, $4, $5, $6, NOW()
)
RETURNING id, profile_id, workflow_id, telegram_user_id, chat_id,
          status, input_data, output_data, error_message,
          started_at, finished_at;

-- name: UpdateExecution :exec
UPDATE telegram_executions
SET status = $2,
    output_data = $3,
    error_message = $4,
    finished_at = CASE WHEN $2 IN ('completed', 'failed') THEN NOW() ELSE finished_at END
WHERE id = $1;

-- name: CreateConversation :one
INSERT INTO telegram_conversations (
    id, profile_id, telegram_user_id, chat_id, context, last_message_at
) VALUES (
    gen_random_uuid(), $1, $2, $3, $4, NOW()
)
RETURNING id, profile_id, telegram_user_id, chat_id, context, last_message_at;

-- name: GetConversation :one
SELECT id, profile_id, telegram_user_id, chat_id, context, last_message_at
FROM telegram_conversations
WHERE profile_id = $1 AND chat_id = $2
LIMIT 1;

-- name: UpdateConversation :exec
UPDATE telegram_conversations
SET context = $2, last_message_at = NOW()
WHERE id = $1;

-- name: GetKnowledgeBase :many
SELECT id, profile_id, source_type, source_id, title, content,
       metadata, embedding, is_active, created_at, updated_at
FROM telegram_knowledge_base
WHERE profile_id = $1 AND is_active = true;

-- name: SearchKnowledge :many
SELECT id, profile_id, source_type, source_id, title, content,
       metadata, is_active,
       1 - (embedding <=> $2::vector) as similarity
FROM telegram_knowledge_base
WHERE profile_id = $1 AND is_active = true
ORDER BY embedding <=> $2::vector
LIMIT $3;

-- name: LogMessage :exec
INSERT INTO telegram_messages_log (
    id, profile_id, telegram_user_id, chat_id, message_text,
    is_from_bot, metadata, created_at
) VALUES (
    gen_random_uuid(), $1, $2, $3, $4, $5, $6, NOW()
);
