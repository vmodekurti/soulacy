# Auth API

## Login

Issue a short-lived JWT.

```
POST /v1/auth/login
```

### Request

```json
{
  "email": "user@example.com",
  "password": "secret"
}
```

### Response

```json
{
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "expires_at": "2026-05-29T12:00:00Z",
  "role": "operator"
}
```

Use the `token` value in subsequent requests: `Authorization: Bearer eyJ...`

---

## Create API key

Create a managed API key (`sk_` prefix). Requires `admin` role.

```
POST /v1/admin/api-keys
Authorization: Bearer sy_your-server-key
```

### Request

```json
{
  "name": "ci-bot",
  "role": "operator"
}
```

### Response

```json
{
  "id": "ak_abc123",
  "name": "ci-bot",
  "key": "sk_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
  "role": "operator",
  "created_at": "2026-05-28T10:00:00Z"
}
```

!!! warning "Save the key"
    The `key` value is shown only once. Store it securely — it cannot be retrieved later.

---

## List API keys

```
GET /v1/admin/api-keys
Authorization: Bearer sy_your-server-key
```

### Response

```json
{
  "keys": [
    {
      "id": "ak_abc123",
      "name": "ci-bot",
      "role": "operator",
      "created_at": "2026-05-28T10:00:00Z",
      "last_used_at": "2026-05-28T11:30:00Z"
    }
  ]
}
```

The plaintext key is never returned after creation.

---

## Revoke API key

```
DELETE /v1/admin/api-keys/{id}
Authorization: Bearer sy_your-server-key
```

### Response

```
204 No Content
```
