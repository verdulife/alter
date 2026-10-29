# Bridge Telegram ↔ Pi — fase 1

> Estado: **implementado en la rama `minimal`**. Las fases 2 (skills) y 3
> (rutinas) no están en este alcance. Este documento describe el comportamiento
> efectivo, no el deseado.

## Qué entrega la fase 1

Un canal directo entre un Pi completo y Telegram: el dueño escribe por Telegram y
Pi contesta con markdown rico (S1-S3). No hay orquestador propio en el camino de
entrada de texto libre; Pi es el motor.

- **Texto rico (S2, S3).** La respuesta verbal de Pi viaja como *rich message*
  (markdown GFM: títulos, listas, tablas, citas, `<details>`, botones). Si Telegram
  rechaza el rich message, se degrada a HTML clásico troceado a 4096 caracteres.
  Mientras Pi piensa, el bridge publica *drafts* (como máximo uno por segundo) y
  una acción `typing`.
- **Archivos entrantes (S4, S6).** Un documento o una foto se descarga, se guarda
  en `<vault>/inbox/` con prefijo de fecha y nombre saneado, y Pi recibe la ruta
  **relativa al vault**. El `caption` es la instrucción; si no hay caption, Pi
  acusa recibo y hace una pregunta corta. Los archivos salientes van con la tool
  `send_file` (fotos como foto, el resto como documento).
- **Preguntas con opciones (S3, S7).** La tool `ask_options` publica una pregunta
  con botones `callback_data`. La pulsación llega por el poller, resuelve la
  espera y **nunca** se convierte en un prompt para Pi.
- **Tools deterministas (S7).** Pi no tiene `bash`, `edit` ni `write`. Lee el vault
  con `read`, `grep`, `find` y `ls` (con `cwd` = vault) y solo escribe a través de
  `save_note`, `append_note`, `send_file` y `ask_options`, servidas por un servidor
  HTTP en `127.0.0.1:0` con token efímero por proceso.
- **Sesión persistente.** Un único proceso `pi --mode rpc` con sesión nombrada, así
  que la conversación conserva el hilo entre mensajes.
- **Un solo chat.** Solo se atiende el chat del dueño (`TELEGRAM_CHAT_ID`); el
  resto de chats y de tipos de evento se ignoran.

## Variables de entorno

Se leen del entorno y, antes, de `.env` en el directorio de trabajo: lo exportado
en la shell siempre gana sobre el fichero, y la ausencia de `.env` no es un error.

| Variable | Defecto | Para qué |
|---|---|---|
| `TELEGRAM_TOKEN` | — (obligatorio) | Token del bot. Sin él el proceso no arranca. |
| `TELEGRAM_CHAT_ID` | — (obligatorio) | Chat del dueño: único destinatario y única fuente aceptada. |
| `ALTER_BRIDGE_ENABLED` | `false` | `true` activa el bridge como **única** ruta de texto libre. |
| `ALTER_BRIDGE_SESSION_ID` | `telegram` | Sesión de Pi que se reanuda entre mensajes. |
| `ALTER_BRIDGE_TOOLS` | `read,grep,find,ls,save_note,append_note,send_file,ask_options` | Allowlist de `--tools`. **Reemplaza** la selección completa de Pi: si quitas las cuatro últimas, Pi no puede escribir ni preguntar nada. |
| `ALTER_BRIDGE_PROMPT_FILES` | `alter-persona.md` | Ficheros markdown para `--append-system-prompt`, en orden. Separador `;`. |
| `ALTER_BRIDGE_EXTENSIONS` | `internal/tools/extension/index.ts` | Extensiones cargadas con `-e`. Separador `;`. |
| `ALTER_BRIDGE_TIMEOUT` | `120s` | Tope de un turno completo (spawn + modelo + tools + respuesta). |
| `ALTER_VAULT_DIR` | `$HOME/vault` | Raíz del vault; también es el `cwd` de Pi. |
| `ALTER_TELEGRAM_API_BASE` | `https://api.telegram.org` | Base URL del Bot API (S8). |
| `ALTER_PI_BIN` | `pi` | Binario de Pi. Un servicio (systemd/cron) casi siempre necesita ruta absoluta porque su `PATH` no incluye fnm. |
| `ALTER_PI_PROVIDER` | vacío | `--provider`; vacío deja que Pi use su configuración. |
| `ALTER_PI_MODEL` | vacío | `--model` (`provider/id[:thinking]`). |
| `ALTER_DB_PATH` | `alter.db` | SQLite de tareas, triggers y eventos. |

Las rutas de `ALTER_BRIDGE_PROMPT_FILES` y `ALTER_BRIDGE_EXTENSIONS` son
**relativas al repositorio**, así que arrancar desde la raíz del repositorio
funciona sin configurar nada. Se admiten rutas absolutas.

## Cómo arrancarlo

```bash
cd /ruta/al/alter
# .env con TELEGRAM_TOKEN y TELEGRAM_CHAT_ID, o exportados en la shell
ALTER_BRIDGE_ENABLED=true \
ALTER_PI_BIN="$HOME/.local/opt/fnm/.../bin/pi" \
ALTER_PI_PROVIDER=anthropic \
ALTER_PI_MODEL='claude-sonnet-4-5' \
go run ./cmd/alter
```

O con un binario:

```bash
go build -o alter ./cmd/alter
./alter
```

Al arrancar se ve en el log qué ruta de texto libre está activa
(`inbound free text = Telegram <-> Pi bridge`). El Scheduler sigue corriendo en
paralelo; `ALTER_PI_ENABLED` decide si sus notificaciones las redacta Pi o el
mensaje plano de Telegram.

**Guardia de arranque.** Con el bridge activo, antes de levantar el servidor de
tools y antes de poder lanzar Pi, se verifica que cada fichero de
`ALTER_BRIDGE_PROMPT_FILES` y `ALTER_BRIDGE_EXTENSIONS` existe y es un fichero
regular. Si falta uno, el proceso muere con un único error que nombra la ruta y la
variable que la configuró:

```
runtime: bridge: extension "internal/tools/extension/index.ts" is not readable (configured by ALTER_BRIDGE_EXTENSIONS): stat ...: no such file or directory
```

Es deliberado: sin la extensión Pi arranca igual y contesta, pero no puede escribir
nada. Ese fallo silencioso es peor que no arrancar. Con el bridge desactivado no se
comprueba nada: el camino antiguo no tiene ficheros que cargar.

## Los dos ficheros que Pi necesita

1. **`alter-persona.md`** — la persona y el contrato de formato (S2, S3). Fija
   idioma y tono, prohíbe volcar código o salidas de herramientas, y explica cómo
   usar tablas, listas, `<details>` y `ask_options`. Sin él Pi responde con su
   prompt por defecto: correcto pero fuera del canal (mensajes largos, código,
   formato que no se lee en el móvil).
2. **`internal/tools/extension/index.ts`** — la extensión que registra
   `save_note`, `append_note`, `send_file` y `ask_options` contra el servidor de
   tools del bridge (`ALTER_TOOLS_URL` + `ALTER_TOOLS_TOKEN`, inyectados por el
   bridge en el entorno de Pi, nunca por el modelo). Sin ella Pi se queda con las
   tools nativas de solo lectura: puede leer el vault y responder, pero no guardar,
   no mandar ficheros y no preguntar con botones.

Ambos se cargan siempre de forma explícita: `--no-extensions` desactiva el
descubrimiento, pero las rutas `-e` siguen cargando.

## Qué está verificado y qué no

**Verificado por tests** (`go test ./...`, con fakes, sin red):

- Render y troceo del markdown, con degradación a HTML, y el cliente del Bot API
  contra `httptest` (todos los métodos que usa el bridge).
- Vault sobre directorios temporales: nombres seguros, frontmatter, límites de
  tamaño y contención de rutas (incluidos symlinks).
- Servidor de tools: token obligatorio, errores de negocio como 4xx, timeout y
  cancelación de `ask_options`.
- Bridge completo contra un RPC falso: drafts, entrega final, línea humana cuando
  Pi no devuelve texto, reintento tras un `getUpdates` fallido, y orden de
  apagado (poller → tools → sesión).
- Archivos entrantes: descarga única, guardado en `inbox/`, rechazo por tamaño
  declarado (sin descargar) y por tamaño real (durante el stream), y que un fallo
  de descarga no tumba el canal.
- `ask_options` de punta a punta: una pregunta, un botón por opción, payload
  opaco, pulsación resuelta y ack al cliente; una segunda pregunta simultánea
  falla en vez de encolarse.
- Integración en un solo runtime (smoke): turno de texto, turno de documento y
  pregunta resuelta por callback, en ese orden, con el documento en el vault y sin
  que la pulsación llegue nunca a Pi.
- Guardia de arranque: falla con ruta inexistente o con un directorio en lugar de
  fichero, y no lanza ningún proceso hijo.

**No verificado (y no se puede verificar con este repo solo):**

- **Bot API real.** Ninguna llamada ha salido a Telegram: ni `sendRichMessage`
  (Bot API 10.1+), ni `sendRichMessageDraft`, ni botones. El formato rico se apoya
  en la documentación, no en una prueba empírica. La primera ejecución real es la
  prueba de verdad.
- **`pi` real y LLM real.** Todo el protocolo RPC se probó contra un proceso falso
  reejecutando el binario de test. No hay evidencia de que el Pi instalado acepte
  exactamente esta combinación de flags (`--no-extensions -e ... --tools ...
  --append-system-prompt ... --session-id ...`).
- **Pulsación física de un botón.** El callback se simula como `update` del fake.
- **Carga de la extensión TypeScript por Pi** y el contrato exacto de
  `pi.registerTool` en la versión desplegada.
- **Systemd, reinicios y larga vida**: no hay prueba de días de ejecución.

## Limitaciones conocidas

- **Los comandos con barra ya no se atienden.** Con `ALTER_BRIDGE_ENABLED=true` el
  adaptador de comandos (`/recordar`, `/tareas`, ayuda…) no se cablea: el bridge es
  la única ruta de entrada. Si necesitas comandos, arranca sin bridge.
- **Una sola pregunta pendiente.** Solo puede haber un `ask_options` en vuelo; una
  segunda pregunta simultánea devuelve error en vez de encolarse, porque el dueño
  solo puede contestar la que está mirando. Sin respuesta en 10 minutos, la tool
  devuelve `sin respuesta` en lugar de colgar el turno.
- **20 MB de entrada.** Es el límite de descarga del Bot API cloud, aplicado dos
  veces: por el tamaño que declara Telegram (antes de descargar) y por los bytes
  reales mientras se guardan. Los envíos de salida se quedan en el límite de subida
  cloud (50 MB).
- **Bot API server propio diferido (S8).** `ALTER_TELEGRAM_API_BASE` permite migrar
  sin tocar código (subida 2000 MB, descarga sin límite, `file://`), pero no está
  desplegado; el server local tendrá que soportar Bot API 10.1+ para no perder los
  rich messages.
- **Un solo dueño.** No hay multi-usuario, ni grupos, ni permisos: cualquier chat
  distinto de `TELEGRAM_CHAT_ID` se ignora.
- **Los bloques de media de los rich messages solo aceptan HTTP/HTTPS**, así que
  los ficheros del vault viajan por `sendDocument`/`sendPhoto` en multipart.

## Siguientes fases

- **Fase 2 — skills.** Un set corto de skills para casos concretos (llega un
  enlace, llega un archivo, tarea o recordatorio) que decidan el flujo sin volver a
  razonarlo cada vez.
- **Fase 3 — rutinas.** Despertar a Pi con un prompt propio mediante cron o un
  daemon, para resúmenes y recordatorios programados que notifican por Telegram.

Ambas se apoyan en lo que ya existe aquí: el vault como almacén (S6), las tools
deterministas (S7) y el canal rico como salida.

## Referencias

- Specs y bitácora de la fase: `odd/tasks/telegram-bridge.md`.
- Contrato de tools (URL, token, errores): sección «Contrato de tools» del mismo
  documento y la cabecera de `internal/tools/extension/index.ts`.
- Persona y reglas de formato: `alter-persona.md`.
