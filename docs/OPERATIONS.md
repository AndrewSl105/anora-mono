# Operations

Health, metrics, logs, retention and restart behaviour of a running Fleetpocket. Back to the [README](../README.md); setup is in [DEPLOY.md](DEPLOY.md).


- **Health**: `GET /healthz` checks Postgres and Docker.
- **Metrics** (`GET /metrics`, internal network only): `fleetpocket_tasks{status}`, `fleetpocket_queue_length`, `fleetpocket_run_duration_seconds{project}`, `fleetpocket_cost_usd_total{project}`, `fleetpocket_runner_exits_total{code}`, `fleetpocket_telegram_api_errors_total{op}`, `fleetpocket_github_api_errors_total{op}`.
- **Logs**: `docker compose -f docker-compose.deploy.yml logs -f server`; JSON (`log/slog`), task lines carry `task_id`, `run_id` and `project`.
- **Retention** (every 10 minutes): agent events of finished tasks older than `EVENT_RETENTION` are deleted (status events stay); workspaces, Claude state volumes and the attachments of failed and cancelled tasks are deleted `WORKSPACE_RETENTION` after a task finishes (a completed task's attachments go right away); attachment files without a task are swept; idle sidecars are removed after `SIDECAR_IDLE_TTL`; expired sessions are purged.
- **Restart safety**: on startup, running agents are re-attached and missed events replayed from `<workspace>/.fleetpocket/events.jsonl`; exits that happened while the server was down are applied, vanished runs fail with reason `lost`, interrupted preparation is re-queued, interrupted pushes retried, orphaned containers and networks removed.
