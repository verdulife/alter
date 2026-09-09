# Pi Agent — protocolo RPC del adapter `internal/adapter/agent/pi.go`

> Estado: **implementado (V1, one-shot)**. El runtime usa el Agent real de Pi cuando
> `ALTER_PI_ENABLED=true`; en caso contrario mantiene `NotifyAction` (Telegram solo).

## Qué es este documento

Describe el contrato mínimo que `alter` usa para invocar Pi como agente. Es un
subconjunto del protocolo RPC oficial de Pi (`pi --mode rpc`, documentado en
`docs/rpc.md` del paquete instalado `@earendil-works/pi-coding-agent`). Este
documento es la referencia del adapter: qué se envía, qué se consume y por qué.

## Ciclo de vida: one-shot por ejecución

Cada `Agent.Execute`:

1. Lanza `pi --mode rpc --no-session [--provider X] [--model Y] [--no-tools] [--append-system-prompt S]`.
   - `--no-session`: ejecución efímera, sin ficheros de sesión.
   - `--provider`/`--model` solo si están configurados (`ALTER_PI_PROVIDER` /
     `ALTER_PI_MODEL` son overrides opcionales; vacíos → Pi usa su configuración).
   - `--no-tools` por defecto (V1 seguro: el agente solo produce texto, sin efectos laterales).
2. Escribe en stdin: `{"type":"prompt","message":"<Instruction>","id":"alter-prompt"}`.
3. Lee eventos de stdout hasta `agent_settled` (fin real del run: sin retries,
   compactación ni continuación pendientes — `agent_end` solo no basta porque Pi
   reintenta internamente errores transitorios).
4. Escribe `{"type":"get_last_assistant_text"}` y usa `data.text` como
   `AgentResult.Response` (nunca se entrega texto vacío).
5. Cierra stdin: Pi termina con exit 0 sobre EOF (verificado empíricamente). Tras
   cerrar stdin se espera al proceso con una gracia acotada (5s) antes de matarlo.

## Frame y campos consumidos

- Framing JSONL estricto: registros separados solo por `\n` (LF). En Go se usa
  `bufio.Scanner` (divide por `\n` a nivel de byte, compatible; a diferencia de
  Node `readline`, que también divide por U+2028/U+2029).
- El adapter **ignora** todo lo demás (message/turn/compaction/queue events).
  Las líneas malformadas se registran y se saltan, nunca son permanentes.
- `extension_ui_request`:
  - Diálogos (`select`, `confirm`, `input`, `editor`) → se responden
    `{"type":"extension_ui_response","id":...,"cancelled":true}` (dismiss).
  - Fire-and-forget (`notify`, `setStatus`, `setWidget`, `setTitle`,
    `set_editor_text`) → se ignoran.
  - `extension_error` → se registra en el logger.

## Clasificación de errores (frontera del adapter)

| Señal | Clasificación | Consecuencia en el Scheduler |
|---|---|---|
| Binario inexistente (`exec.ErrNotFound` o ENOENT) | **permanente** (`ErrActionPermanent`) | Retira el trigger (config de entorno: fija `ALTER_PI_BIN`) |
| `prompt` rechazado (`response command=prompt success:false`) | **permanente** (`ErrActionPermanent`) | Retira el trigger (config/contrato: modelo, auth, parse) |
| Timeout (`ALTER_PI_TIMEOUT`) | retryable | `RetryAt` backoff |
| Crash/EOF antes de `agent_settled` | retryable | `RetryAt` backoff |
| `auto_retry_end success:false` (retries internos agotados) | retryable | `RetryAt` backoff |
| Run settleado sin texto de respuesta | retryable | `RetryAt` backoff |
| Cualquier otro fallo de IO/spawn | retryable | `RetryAt` backoff |

## Variables de entorno (config)

| Variable | Default | Significado |
|---|---|---|
| `ALTER_PI_ENABLED` | `false` | Opt-in explícito al Agent real |
| `ALTER_PI_BIN` | `pi` (PATH) | Ruta del binario; usar absoluta fuera de shells (systemd/cron no ven fnm) |
| `ALTER_PI_PROVIDER` | vacío | Override opcional de provider (`--provider`) |
| `ALTER_PI_MODEL` | vacío | Override opcional de modelo (`--model`) |
| `ALTER_PI_TIMEOUT` | `60s` | Límite de una ejecución completa |
| `ALTER_PI_NO_TOOLS` | `true` | `--no-tools`; `false` rehabilita tools explícitamente |
| `ALTER_PI_SYSTEM_PROMPT` | vacío | Prompt de sistema extra (`--append-system-prompt`) |

## Wiring en el runtime

`cmd/alter/main.go`: si `ALTER_PI_ENABLED=true` → acción del Scheduler =
`agent.NewOrchestrator(PiAgent, Channel, EventStore)` (composite
Agent → Channel → Event best-effort). Si no → `telegram.NewNotifyAction`.
No existe ningún fake Agent en producción; los fakes viven solo en tests
(`TestPiRPCFake` re-ejecuta el binario de test como un Pi RPC simulado).

## Fuera de V1

Proceso Pi persistente/pool, SDK Node, sesiones/steer/follow-up, Semantic Search
(el seam es `Instruction`, derivada por el Orchestrator), dedup/idempotencia.