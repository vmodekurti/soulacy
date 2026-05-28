# WhatsApp

Connect agents to WhatsApp using the WhatsApp Business Platform (Meta Cloud API).

## Prerequisites

- A **Meta Business Account**
- A **WhatsApp Business Account** linked to it
- A phone number approved for WhatsApp Business API

> **Note:** WhatsApp Business API is not free — it is billed per conversation. See [Meta's pricing](https://developers.facebook.com/docs/whatsapp/pricing).

## Setup

### 1. Create a Meta App

1. Go to [developers.facebook.com](https://developers.facebook.com) → **My Apps** → **Create App**
2. Select **Business** app type
3. Add the **WhatsApp** product

### 2. Configure the phone number

In the WhatsApp section of your app, add and verify your business phone number.

### 3. Get credentials

- **Access Token** — permanent token from Meta Business Suite
- **Phone Number ID** — the ID of your WhatsApp phone number
- **Webhook Verify Token** — any secret string you choose

### 4. Configure Soulacy

```yaml title="config.yaml"
channels:
  whatsapp:
    access_token: "EAA..."
    phone_number_id: "123456789"
    verify_token: "my-webhook-secret"
    webhook_path: /webhooks/whatsapp
    agents:
      - assistant
    default_agent: assistant
```

### 5. Register the webhook

In your Meta app under **WhatsApp → Configuration**, set:

- **Callback URL**: `https://yourdomain.com/webhooks/whatsapp`
- **Verify Token**: the same string as `verify_token` above
- Subscribe to: `messages`

---

## Features

| Feature | Support |
|---------|---------|
| Text messages | ✅ |
| Images | ✅ (passed to vision agents) |
| Documents | ✅ (text extracted) |
| Audio | 🔜 Planned |
| Rich formatting | ❌ (WhatsApp is plain text only) |
| Templates | 🔜 Planned |

---

## Limitations

- WhatsApp does not support markdown, bold, or links in outgoing messages — responses are sent as plain text
- The business phone number cannot receive regular WhatsApp messages once it's on the API
- 24-hour conversation window applies: you can only reply within 24 hours of the last user message (or use approved message templates)
