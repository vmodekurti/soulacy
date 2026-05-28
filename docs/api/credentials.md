# Credentials API

The credential vault stores sensitive values (API keys, tokens, passwords) encrypted with AES-256-GCM. Agents can reference stored credentials by name at runtime without embedding secrets in SOUL.yaml.

## Store a credential

```
POST /v1/credentials
Authorization: Bearer sy_your-server-key
Content-Type: application/json
```

### Request

```json
{
  "name": "openai-prod",
  "value": "sk-...",
  "description": "OpenAI production API key"
}
```

### Response

```json
{
  "id": "cred_abc123",
  "name": "openai-prod",
  "description": "OpenAI production API key",
  "created_at": "2026-05-28T10:00:00Z"
}
```

The plaintext `value` is never returned after creation.

---

## List credentials

```
GET /v1/credentials
Authorization: Bearer <token>
```

### Response

```json
{
  "credentials": [
    {
      "id": "cred_abc123",
      "name": "openai-prod",
      "description": "OpenAI production API key",
      "created_at": "2026-05-28T10:00:00Z",
      "last_rotated_at": null
    }
  ]
}
```

---

## Delete a credential

```
DELETE /v1/credentials/{id}
Authorization: Bearer sy_your-server-key
```

### Response

```
204 No Content
```

---

## Rotate a credential

```
PUT /v1/credentials/{id}/rotate
Authorization: Bearer sy_your-server-key
Content-Type: application/json
```

### Request

```json
{
  "value": "sk-new-key-value"
}
```

### Response

```json
{
  "id": "cred_abc123",
  "name": "openai-prod",
  "rotated_at": "2026-05-28T12:00:00Z"
}
```

---

## Using credentials in SOUL.yaml

Reference a stored credential by name in agent configuration:

```yaml
name: my-agent
model: gpt-4o
credentials:
  openai_key: openai-prod   # credential name from the vault
```

At runtime, Soulacy substitutes the decrypted value before passing it to the LLM provider.
