# alter

Secretario personal por Telegram. El motor es Pi; el almacén es el vault.

## Qué es hoy

Un único proceso: el puente Telegram ↔ Pi más el vault. El dueño escribe por
Telegram y un `pi --mode rpc` persistente —con la configuración global de Pi y las
skills del proyecto— contesta con markdown rico (tablas, listas, citas, botones).
Solo dos tools son propias de Alter, `send_file` y `ask_options`; el resto es la
caja de herramientas de una sesión de terminal (`bash`, `write`, `edit`, `read`,
`grep`, `find`, `ls`) y las notas se escriben con `write`/`edit`.

No hay base de datos, ni scheduler, ni búsqueda semántica, ni una segunda ruta de
entrada: se eliminaron, no se desactivaron. Tampoco hay dependencias externas en
`go.mod`.

- **El vault es el almacén.** Tareas, recordatorios, eventos y notas son markdown
  con frontmatter plano dentro de `ALTER_VAULT_DIR` (por defecto `$HOME/vault`),
  uno por fichero, en `tasks/`, `reminders/`, `events/` y `notes/`. El contrato de
  cada entrada vive en [`.agents/skills/alter-vault/SKILL.md`](.agents/skills/alter-vault/SKILL.md),
  no en código.
- **Nada los dispara todavía.** Un recordatorio o una tarea con `remind_at` se
  guardan como markdown y se leen a mano; que el sistema avise solo es la fase
  siguiente (rutinas programadas). No hay cron ni daemon.
- **`/reset` reinicia la conversación** sin reiniciar el servicio: cierra el
  proceso `pi` y el turno siguiente arranca con una sesión nueva. Es el único
  comando de barra; cualquier otro texto, `/reset algo` incluido, va a Pi como
  prompt.
- **`alter vault check [dir]` valida las entradas**: recorre esas cuatro carpetas,
  imprime un problema por línea (`<ruta>:<línea>: <mensaje>`) en stderr, resume en
  stdout y sale con 1 si alguna entrada no cumple el contrato (2 si el vault no se
  puede leer).

Detalle operativo completo —variables, arranque, qué está probado y qué no,
limitaciones—: [`docs/bridge-fase1.md`](docs/bridge-fase1.md). Límites de producto
y reglas de aprobación: [`docs/alter-limites.md`](docs/alter-limites.md).

## Arranque

Corre como unit de usuario, `alter.service`, con el repositorio como
`WorkingDirectory` y su propio `.env`:

```bash
systemctl --user start alter.service
scripts/alterctl.sh status     # status | start | stop | restart | preflight
```

`scripts/alterctl.sh` comprueba la identidad (unit, binario, `.env`, `getMe`)
antes de parar o reiniciar nada; las reglas de aislamiento están en
[`docs/operational-isolation.md`](docs/operational-isolation.md).

La configuración se lee del entorno y, antes, de `.env` en el directorio de
trabajo: lo exportado en la shell gana y que falte `.env` no es un error. Los dos
obligatorios son `TELEGRAM_TOKEN` y `TELEGRAM_CHAT_ID`; el resto tiene defecto y
vive bajo `ALTER_BRIDGE_*`:

| Variable | Defecto |
|---|---|
| `TELEGRAM_TOKEN` | — (obligatorio) |
| `TELEGRAM_CHAT_ID` | — (obligatorio) |
| `ALTER_BRIDGE_SESSION_ID` | `telegram` |
| `ALTER_BRIDGE_TIMEOUT` | `300s` |
| `ALTER_BRIDGE_WORKDIR` | directorio de trabajo del proceso (el repositorio) |
| `ALTER_BRIDGE_TOOLS` | vacío: sin `--tools`, Pi conserva su selección completa |
| `ALTER_BRIDGE_PROMPT_FILES` | `alter-persona.md` |
| `ALTER_BRIDGE_EXTENSIONS` | `internal/tools/extension/index.ts` |
| `ALTER_VAULT_DIR` | `$HOME/vault` |
| `ALTER_TELEGRAM_API_BASE` | `https://api.telegram.org` |
| Pi: binario, proveedor, modelo (`ALTER_PI_*`: `BIN`, `PROVIDER`, `MODEL`) | `pi` en el `PATH`; sin overrides |

Las rutas de `ALTER_BRIDGE_PROMPT_FILES` y `ALTER_BRIDGE_EXTENSIONS` son
relativas al repositorio. Al arrancar se comprueba que cada una existe y es un
fichero regular, y si falta una el proceso muere con un error que nombra la ruta
y la variable: un Pi sin persona o sin las tools de Alter arrancaría igual y
fallaría en silencio.

Sin la unit, en primer plano (útil para probar):

```bash
TELEGRAM_TOKEN=... TELEGRAM_CHAT_ID=... go run ./cmd/alter
go build -o alter ./cmd/alter    # binario
./alter vault check              # validar el vault y salir
```

## Estructura

```
alter/
├── cmd/alter/                      # Entry point, wiring del bridge, guardia y `vault check`
├── internal/
│   ├── adapter/telegram/           # Cliente Bot API, poller, canal y rich messages
│   ├── bridge/                     # Sesión Pi persistente (pi --mode rpc)
│   ├── config/                     # Configuración por entorno + .env
│   ├── tools/                      # Servidor de tools y extensión Pi (extension/index.ts)
│   └── vault/                      # Almacén de notas y validador del contrato
├── .agents/skills/alter-vault/     # Contrato del vault (una skill de proyecto)
├── alter-persona.md                # Persona y contrato de formato de Pi
├── scripts/                        # alterctl.sh (operación) y gate-check.mjs (gate)
├── docs/
└── README.md
```

`odd/` guarda la bitácora de la fase y `docs/archive/` los diseños descartados
—scheduler y Pi Agent one-shot—, que describen un runtime que ya no existe.

## Requisitos

- Go 1.25+
- Git
- El CLI `pi` en el `PATH` (o su ruta absoluta configurada para el servicio)
