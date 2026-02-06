# Secrets

Эта папка содержит чувствительные данные (пароли, токены).

## Для локальной разработки:

Создайте файл `redis_password.txt` с паролем Redis:

```bash
echo "9;S%oH!-dVvPZ:" > secrets/redis_password.txt
```

## Для production (Docker Swarm):

Используйте Docker secrets:

```bash
echo "9;S%oH!-dVvPZ:" | docker secret create redis_password -
```

И в docker-compose.yml укажите:

```yaml
secrets:
  redis_password:
    external: true
```

⚠️ Файлы с паролями не должны попадать в git!
