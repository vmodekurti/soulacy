# Plugin Database Migrations (Story E16)

Plugins can own SQLite schema without ever touching the core system
databases. Schema steps register through the SDK and apply transactionally
during the database boot phase, into the **dedicated plugin database**
(`~/.soulacy/plugins.db`).

## Registering

```go
import "github.com/soulacy/soulacy/sdk/storage"

func init() {
    storage.MustRegisterMigration("weather-bot", "001_create_cache",
        `CREATE TABLE plugin_weather_bot_cache (
            city       TEXT PRIMARY KEY,
            payload    TEXT NOT NULL,
            fetched_at DATETIME NOT NULL
        )`)
    storage.MustRegisterMigration("weather-bot", "002_index",
        `CREATE INDEX plugin_weather_bot_cache_at ON plugin_weather_bot_cache(fetched_at)`)
}
```

Compiled-in plugins are linked through the generated blank-import file
(`cmd/soulacy/builtins_gen.go`) — the same mechanism as channel/provider/
strategy drivers (E10/E15) and what the E12 flavored-binary tool automates.

## Rules (enforced before any SQL executes)

1. **Namespace.** Every table a migration creates or touches must be
   prefixed `plugin_<id>_` (id sanitised: non-alphanumerics → `_`).
   Index/trigger targets resolve to their backing table; foreign and core
   tables (`token_usage`, `agent_events`, `conversation_history`,
   `workboard_*`, `credentials`, `rbac*`, …) are refused. This is the E5
   plugin-principal model applied to schema.
2. **Statement kinds.** Allowed: `CREATE TABLE/INDEX/TRIGGER/VIEW`,
   `ALTER TABLE`, `DROP`, `INSERT`, `UPDATE`, `DELETE`. Refused anywhere:
   `ATTACH`, `DETACH`, `PRAGMA`, `VACUUM`, `REINDEX`, `load_extension`.
3. **Applied exactly once.** Bookkeeping keys on `(plugin_id, name)` with a
   SHA-256 checksum — editing an applied migration's SQL is an error;
   register a new name instead.
4. **Transactional.** Each step is one transaction; failure rolls back
   atomically, is not recorded, and stops that plugin's remaining steps.
   Other plugins continue (warn-and-skip, like the manifest loader).
5. **No down migrations.** Write additive steps.

## Boot flow

`internal/app` (Run, right after the storage backends come up) collects
`storage.RegisteredMigrations()` and applies pending steps via
`internal/pluginmigrate.Runner`. Failures log
`plugin migration refused or failed` warnings; the gateway always boots.
