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
- **Tools deterministas (S7, T17).** Pi tiene la **misma caja de herramientas que
  en una sesión de terminal** (T11): `bash`, `edit`, `write`, `read`, `grep`,
  `find`, `ls`, más todo lo que aporten los paquetes cargados (memoria, web,
  subagentes). Solo dos tools son propias de Alter, servidas por un servidor HTTP
  en `127.0.0.1:0` con token efímero por proceso: `send_file`, para entregar al
  dueño un archivo del vault, y `ask_options`, para preguntar con botones. Las
  notas no tienen tool propia: Pi escribe el vault con `write` y `edit`, que es lo
  que ya sabe hacer. Ya no hay allowlist forzada ni restricción de solo lectura: la
  contención es la identidad y las aprobaciones (T13).
- **Carga global de Pi (T11).** El bridge **no** desactiva el descubrimiento: no se
  pasan `--no-extensions`, `--no-skills`, `--no-prompt-templates`, `--no-themes`
  ni `--no-context-files`, así que Pi carga la misma configuración global que en
  una terminal (gentle-pi, gentle-engram, pi-btw, pi-web-access, sus skills y los
  ficheros de contexto). Las rutas `-e` y los `--append-system-prompt` del bridge
  son **aditivos** a lo descubierto, no un reemplazo.
- **Directorio de trabajo (T11, T18).** Pi arranca con `cwd` =
  `ALTER_BRIDGE_WORKDIR`, que por defecto es el directorio de trabajo del proceso
  (el repositorio, que es donde corre el servicio). Desde ahí descubre las skills
  del proyecto. Ve todo lo que ves tú: `bash` llega a cualquier ruta absoluta y el
  vault sigue en `$HOME/vault`. El vault ya **no** es el `cwd`: es solo el almacén
  que Pi escribe con `write`/`edit` y del que `send_file` lee.
- **Skills del proyecto (T18).** Las skills de Alter, como `alter-vault`, viven en
  `.agents/skills/` del repositorio, no en la configuración global: Pi las
descubre desde su directorio de trabajo hacia arriba, así que solo existen para
  Alter y solo cuestan su nombre y descripción hasta que el modelo las invoca.
- **Sesión persistente.** Un único proceso `pi --mode rpc` con sesión nombrada, así
  que la conversación conserva el hilo entre mensajes.
- **Timeout amplio.** El tope por turno son 300 s por defecto
  (`ALTER_BRIDGE_TIMEOUT`), porque un turno con `bash` puede tardar.
- **Un solo chat.** Solo se atiende el chat del dueño (`TELEGRAM_CHAT_ID`); el
  resto de chats y de tipos de evento se ignoran.

## Un solo runtime, sin base de datos

El binario `alter` es una sola cosa: el puente Telegram ↔ Pi más el vault. No hay
segunda ruta de entrada, ni SQLite, ni scheduler, ni búsqueda semántica; el
registro de capacidades y el camino antiguo de comandos se han eliminado, no
desactivado.

- **Sin base de datos.** No se abre ningún fichero SQLite y `ALTER_DB_PATH` ya no
existe: al arrancar no se crea nada en disco. `go.mod` no tiene dependencias
externas.
- **Sin trabajo programado.** El scheduler y sus servicios desaparecen con la base
de datos. Tareas, recordatorios y eventos viven como notas markdown con
frontmatter **en el vault**; el contrato está en la skill de proyecto
`alter-vault`, no en código.
- **Un único camino de arranque.** `ALTER_BRIDGE_ENABLED` deja de ser un
interruptor. Si sigue en el entorno con un valor que antes apagaba el bridge, el
proceso escribe una línea diciendo que se ignora y arranca igual: el bridge no se
puede desactivar porque no hay nada más.
- **Arranque fail-closed.** Si falta `TELEGRAM_TOKEN`, `TELEGRAM_CHAT_ID` o alguno
de los dos ficheros que Pi necesita, el proceso muere con un solo mensaje que
nombra la variable a corregir.

## Variables de entorno

Se leen del entorno y, antes, de `.env` en el directorio de trabajo: lo exportado
en la shell siempre gana sobre el fichero, y la ausencia de `.env` no es un error.

| Variable | Defecto | Para qué |
|---|---|---|
| `TELEGRAM_TOKEN` | — (obligatorio) | Token del bot. Sin él el proceso no arranca. |
| `TELEGRAM_CHAT_ID` | — (obligatorio) | Chat del dueño: único destinatario y única fuente aceptada. |
| `ALTER_BRIDGE_ENABLED` | ignorada | Ya no es un interruptor: el bridge es siempre el runtime. Un valor distinto de "true" produce una línea de aviso al arrancar y nada más. |
| `ALTER_BRIDGE_SESSION_ID` | `telegram` | Sesión de Pi que se reanuda entre mensajes. |
| `ALTER_BRIDGE_TOOLS` | vacío (sin `--tools`) | Allowlist **opcional** de `--tools`. Vacío no pasa el flag, así que Pi conserva su selección completa por defecto. Si lo rellenas, **reemplaza** la selección entera: nombra también `send_file` y `ask_options` o Pi no podrá usarlas. |
| `ALTER_BRIDGE_PROMPT_FILES` | `alter-persona.md` | Ficheros markdown para `--append-system-prompt`, en orden. Separador `;`. |
| `ALTER_BRIDGE_EXTENSIONS` | `internal/tools/extension/index.ts` | Extensiones cargadas con `-e`. Separador `;`. |
| `ALTER_BRIDGE_TIMEOUT` | `300s` | Tope de un turno completo (spawn + modelo + tools + respuesta). |
| `ALTER_BRIDGE_WORKDIR` | directorio de trabajo del proceso (el repositorio) | `cwd` del proceso de Pi: su directorio de trabajo, no el vault. De ahí para arriba se descubren las skills del proyecto; si no se puede resolver, cae a `$HOME`. |
| `ALTER_VAULT_DIR` | `$HOME/vault` | Raíz del vault: el almacén que Pi escribe con `write`/`edit` y donde caen los archivos entrantes. No es el `cwd` de Pi. |
| `ALTER_TELEGRAM_API_BASE` | `https://api.telegram.org` | Base URL del Bot API (S8). |
| `ALTER_PI_BIN` | `pi` | Binario de Pi. Un servicio (systemd/cron) casi siempre necesita ruta absoluta porque su `PATH` no incluye fnm. |
| `ALTER_PI_PROVIDER` | vacío | `--provider`; vacío deja que Pi use su configuración. |
| `ALTER_PI_MODEL` | vacío | `--model` (`provider/id[:thinking]`). |

Las rutas de `ALTER_BRIDGE_PROMPT_FILES` y `ALTER_BRIDGE_EXTENSIONS` son
**relativas al repositorio**, así que arrancar desde la raíz del repositorio
funciona sin configurar nada. Se admiten rutas absolutas. La guardia de arranque
las resuelve contra el `cwd` del propio bridge (el repositorio), **nunca** contra
`ALTER_BRIDGE_WORKDIR`: los ficheros pertenecen al repositorio y una configuración
explícita del directorio de trabajo no debe romper el arranque.

## Validación del vault

El contrato de las entradas vive en la skill `alter-vault`, pero el esquema no se
protege solo: Pi escribe esos ficheros con `write` y `edit`, y un frontmatter mal
formado solo aparecería como un fallo silencioso del futuro cron. `alter vault
check [dir]` recorre `tasks/`, `reminders/`, `events/` y `notes/` e ignora el
resto, valida cada entrada contra el contrato (frontmatter plano delimitado,
claves conocidas, enums, fechas RFC3339 con desfase, etiquetas en línea y campos
obligatorios de cada tipo, incluido que un recordatorio sin `remind_at` no vale),
imprime un problema por línea como `<ruta>:<línea>: <mensaje>` en stderr más un
resumen en stdout, y sale con 1 si hay alguna entrada inválida y 0 si están todas
limpias. Sin `dir` usa `ALTER_VAULT_DIR` y, si no está definido, `$HOME/vault`; un
vault que no existe es error de uso (2), no una ejecución limpia. Es el comando
que el daemon de la fase 3 llamará antes de fiarse de una entrada, y el mismo que
se puede ejecutar a mano.

## Cómo arrancarlo

```bash
cd /ruta/al/alter
# .env con TELEGRAM_TOKEN y TELEGRAM_CHAT_ID, o exportados en la shell
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

Al arrancar se ve en el log el único runtime que existe
(`runtime: alter 0.1.0 started (vault=…, workdir=…, session=…)`). No hay nada más
corriendo en paralelo: sin base de datos y sin scheduler, el proceso solo atiende
el chat del dueño.

**Guardia de arranque.** Con el bridge activo, antes de levantar el servidor de
tools y antes de poder lanzar Pi, se verifica que cada fichero de
`ALTER_BRIDGE_PROMPT_FILES` y `ALTER_BRIDGE_EXTENSIONS` existe y es un fichero
regular. Si falta uno, el proceso muere con un único error que nombra la ruta y la
variable que la configuró:

```
runtime: bridge: extension "internal/tools/extension/index.ts" is not readable (configured by ALTER_BRIDGE_EXTENSIONS): stat ...: no such file or directory
```

Es deliberado: sin la extensión Pi arranca igual y contesta, pero no puede mandar
un archivo del vault ni preguntar con botones. Ese fallo silencioso es peor que no
arrancar. La guardia corre siempre: el bridge es el único camino que queda, así que
no hay un modo en el que no haya ficheros que cargar.

## Los dos ficheros que Pi necesita

1. **`alter-persona.md`** — la persona y el contrato de formato (S2, S3). Fija
   idioma y tono, prohíbe volcar código o salidas de herramientas, y explica cómo
   usar tablas, listas, `<details>` y `ask_options`. Sin él Pi responde con su
   prompt por defecto: correcto pero fuera del canal (mensajes largos, código,
   formato que no se lee en el móvil).
2. **`internal/tools/extension/index.ts`** — la extensión que registra
   `send_file` y `ask_options` contra el servidor de tools del bridge
   (`ALTER_TOOLS_URL` + `ALTER_TOOLS_TOKEN`, inyectados por el bridge en el entorno
   de Pi, nunca por el modelo). Sin ella Pi conserva sus tools nativas y puede
   escribir el vault con `write`/`edit`, pero no manda ficheros ni pregunta con
   botones.

Ambos se cargan siempre de forma explícita con `-e` y `--append-system-prompt`,
**además** de lo que Pi descubra por su cuenta en tu configuración global.

## Permisos y ámbito (T11)

El bridge **no** recorta a Pi. Arranca con `--mode rpc`, las rutas `-e` del
bridge, la sesión nombrada y los `--append-system-prompt`; todo lo demás lo decide
Pi con tu configuración global. `--tools` solo aparece si configuras
`ALTER_BRIDGE_TOOLS` a mano. **No** se pasa `--no-approve`: las confirmaciones se
responden por botones.

Las consecuencias son las que declara `docs/alter-limites.md`: con `bash` y `write`
en `$HOME`, un mensaje puede tocar cualquier cosa tuya. La frontera real es el gate
de aprobación (todo `bash`, `write` y `edit` pide botón) más el token del bot y tu
chat ID.

## Qué está verificado y qué no

**Verificado por tests** (`go test ./...`, con fakes, sin red):

- Render y troceo del markdown, con degradación a HTML, y el cliente del Bot API
  contra `httptest` (todos los métodos que usa el bridge).
- Vault sobre directorios temporales: nombres seguros, frontmatter, límites de
  tamaño y contención de rutas (incluidos symlinks).
- Servidor de tools: token obligatorio, errores de negocio como 4xx, timeout y
  cancelación de `ask_options`, contención de rutas en `send_file` (incluidos
  symlinks) y que las dos rutas de escritura retiradas en T17 ya no existen.
- Bridge completo contra un RPC falso: drafts, entrega final, línea humana cuando
  Pi no devuelve texto, reintento tras un `getUpdates` fallido, y orden de
  apagado (poller → tools → sesión).
- Ámbito del bridge (T11, T18): los argumentos no llevan ningún `--no-*`, un
  `ALTER_BRIDGE_TOOLS` vacío **no** emite `--tools`, el `cwd` del hijo es
  `ALTER_BRIDGE_WORKDIR` (nunca el vault), el defecto es el directorio de trabajo
  del proceso con caída a `$HOME`, y el defecto de timeout es 300 s.
- Aprobaciones (T13): un diálogo `confirm` llega al bridge y se convierte en dos
  botones; un `select` en una lista de botones; `input`/`editor` se cancelan con
  traza. Sin respuesta, error o cancelación: `cancelled`, nunca permitido.
  `ask_options` y las aprobaciones comparten un único hueco pendiente. Los turnos
  corren fuera del bucle de polling, porque un bucle bloqueado dentro de un turno
  no podría recibir la pulsación que lo desbloquea.
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
  exactamente esta combinación de flags (`-e ... --session-id ...
  --append-system-prompt ...`, sin ningún `--no-*`), ni de
  que cargue la configuración global descubierta sin degradar el arranque.
- **Pulsación física de un botón.** El callback se simula como `update` del fake.
- **Carga de la extensión TypeScript por Pi** y el contrato exacto de
  `pi.registerTool` en la versión desplegada.
- **Systemd, reinicios y larga vida**: no hay prueba de días de ejecución.

## Limitaciones conocidas

- **Los comandos con barra ya no se atienden.** El adaptador de comandos
  (`/recordar`, `/tareas`, ayuda…) se eliminó junto con el runtime de base de
datos: todo texto libre va a Pi y no hay forma de volver atrás.
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
