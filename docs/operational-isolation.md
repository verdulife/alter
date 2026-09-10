# Aislamiento operativo de ALTER

> Auditoría de aislamiento (septiembre 2026). ALTER debe operarse de forma
> inequívoca, sin confundirse jamás con otros servicios de Telegram del mismo
> servidor (en particular `chistes` / bot `BalnearioServerBot`).

## Identidad de ALTER

| Aspecto | Valor |
|---|---|
| unit systemd | `~/.config/systemd/user/alter.service` (`systemctl --user`) |
| binario canónico | `/home/balneario/alter/alter` (nunca `/tmp`) |
| WorkingDirectory | `/home/balneario/alter` |
| configuración | `/home/balneario/alter/.env` (EXCLUSIVA de ALTER) |
| bot Telegram | `AlterYouBot` (verificado con `getMe`) |
| chat objetivo | `TELEGRAM_CHAT_ID` del `.env` de ALTER |
| gestión | `scripts/alterctl.sh` (status/start/stop/restart/preflight) |

## Reglas obligatorias

- Las operaciones de ALTER se realizan **exclusivamente** con
  `systemctl --user ... alter.service` (o `scripts/alterctl.sh`).
- **Nunca** `pgrep`/`pkill`/`killall` ni matar procesos por nombre para
  servicios de Telegram.
- Identidad verificada antes de cualquier `stop`/`restart`/E2E:
  unit exacta, WorkingDirectory/ExecStart esperados, MainPID vía `systemctl`,
  binario canónico, `.env` exacto, `getMe` == `AlterYouBot`, `TELEGRAM_CHAT_ID`
  del `.env`. Cualquier fallo → abortar sin tocar nada.
- El token de ALTER **jamás** se imprime, registra ni se comparte.

## Pertenencia de scripts (NO usar en ALTER)

| Script | Pertenece a |
|---|---|
| `~/scripts/tg.sh` | **chistes** / bot `BalnearioServerBot` |
| `~/scripts/machine-status-notify.sh` | **chistes** / bot `BalnearioServerBot` |

- Nunca reutilizar token, `.env`, PID, script ni proceso de `chistes` para ALTER.
- No tocar `chistes-bot.service` ni el bot `BalnearioServerBot`.

## Operación

```bash
# Estado y verificación de identidad
~/alter/scripts/alterctl.sh status
~/alter/scripts/alterctl.sh preflight

# Ciclo de vida
~/alter/scripts/alterctl.sh start
~/alter/scripts/alterctl.sh stop
~/alter/scripts/alterctl.sh restart
```