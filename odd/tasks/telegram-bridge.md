# Telegram ↔ Pi bridge — fase 1 (canal completo)

Feature branch: `minimal` (desde `master` @ `5a3444e`). Alcance: fase 1 de la idea
de Verdu — canal bidireccional rico entre Pi y Telegram. Fases 2 (skills) y 3
(rutinas) quedan fuera.

## Specs

**S1 — Canal directo.** Un canal de comunicación directo entre un Pi completo
(gentle-shell) y Telegram. Pi es el motor: no hay orquestador propio en el camino
de entrada de texto libre.

**S2 — Solo lo verbal.** Se envía al usuario la parte verbal de la respuesta; el
contenido de código (diffs, ficheros, salidas de herramientas) no se reenvía
como tal salvo que forme parte de la respuesta redactada.

**S3 — Mensajes ricos.** Texto con formato (negrita, cursiva, código inline,
listas, citas, enlaces), **markdown**, **tablas**, **preguntas** y **opciones**
seleccionables. Debe sentirse como una terminal con Pi, no como un chat plano.
Se implementa con **Bot API Rich Messages** (10.1+): `sendRichMessage` acepta el
markdown de Pi tal cual (GFM-compatible: títulos, listas, task lists, tablas
nativas, blockquotes, footnotes, fórmulas, `<details>`), con 32768 caracteres y
500 bloques por mensaje, y opciones como `<tg-button type="callback_data">`. El
HTML clásico (`parse_mode: HTML`, 4096 caracteres por trozo) queda como
**fallback** cuando Telegram rechaza el rich message.

**S4 — Archivos en ambos sentidos.** El dueño puede enviar archivos por Telegram
y Pi recibirlos; Pi puede enviar archivos al dueño. Los bloques de media de un
rich message admiten **solo HTTP/HTTPS**, así que los archivos del vault viajan
por `sendDocument`/`sendPhoto` en multipart, no como bloque de media.

**S5 — Enlaces en ambos sentidos.** El dueño puede enviar enlaces; Pi puede
enviar enlaces con preview.

**S6 — Vault como almacén.** El vault global del servidor
(`/home/balneario/vault`, localizado en esta sesión) almacena notas, tareas,
listas y algunos archivos. El acceso es completo, así que pueden usarse otras
carpetas para archivos.
Convención existente a respetar (ya usada por `vault/scripts/save-ia-url.sh`):
frontmatter YAML (`title`, `description`, `tags`, `date`) + `# H1` + secciones,
y entradas de enlace como `- [título](url) — metadata`.

**S7 — Pi opera con sus herramientas completas.** Sin allowlist impuesta y con
la configuración global de Pi cargada: `bash`, `edit`, `write`, `read`, `grep`,
`find`, `ls` y las herramientas de los paquetes instalados. Las cuatro tools
deterministas (`save_note`, `append_note`, `send_file`, `ask_options`) siguen
existiendo como vía preferente para el vault, no como la única vía. Retirado por
L9: la versión anterior de este spec exigía solo lectura y cwd = vault.
**S8 — Bot API server propio diferido.** La base URL de Telegram es
configurable (`ALTER_TELEGRAM_API_BASE`, default `https://api.telegram.org`) para
poder migrar al Bot API server local (`--local`: subida 2000 MB, descarga sin
límite, `file://`) sin tocar código. El límite de la API cloud (50 MB subida,
20 MB descarga) se acepta en esta fase. Requisito para la migración futura: el
server local debe soportar Bot API 10.1+ para no perder Rich Messages.

## Tasks

**Estrategia de entrega:** el total de la fase 1 excede con holgura un diff
revisable, así que se entrega en slices por *capacidad observable*, no por
acumulación. Slices cerrados:

| Slice | Contenido | Commit |
|---|---|---|
| 1 | T1 (config) + documento de feature | `f0861e4` |
| 2 | T2 (cliente Bot API) | `544c3ba` |
| 3 | T3 (salida rica + fallback) | `379bce0` |
| 4 | T4 (poller de entrada) | `eccfead` |
| 5 | T5 (vault) | `1b8a6c6` |
| 6 | T8 (persona y reglas de formato) | `cf8c6f2` |
| 7 | T6 (sesión Pi persistente) | `d8f7e0a` + `1c23a36` |
| 8 | T7 (tools deterministas + fix de contención) | `d0979fa` + `186ac4c` |
| 9 | T9a (wiring de texto) | `9bb4659` |
| 10 | T9c (archivos entrantes) | `ee25faa` |
| 11 | T9b (ask_options) | `197409f` |
| 12 | T10 (guardia, smoke, docs) | siguiente |

Siguiente corte: **T5 (vault)** y **T6 (proceso Pi)**, cada uno un slice propio.

Si en algún momento se abre PR, va partido en chained PRs siguiendo
`gentle-ai-chained-pr`: este documento y los slices 1-3 no caben en una sola
revisión de 400 líneas. La rama `minimal` no se publica sin decisión explícita
de Verdu.

| ID | Tarea | Specs | Ruta | Commit |
|---|---|---|---|---|
| T1 | Config: `ALTER_TELEGRAM_API_BASE`, `ALTER_VAULT_DIR`, `ALTER_BRIDGE_*`, carga de `.env` | S1,S6,S8 | inline | — |
| T2 | Cliente Bot API: `sendRichMessage`, `sendRichMessageDraft`, `sendMessage` HTML (fallback), `sendDocument` por ruta, `sendPhoto`, `getFile`+descarga, `sendChatAction`, `answerCallbackQuery` | S3,S4,S5,S8 | inline | — |
| T3 | Salida rica: markdown directo a `sendRichMessage`, validación de límites (32768 chars / 500 bloques / 20 columnas) con degradación a HTML clásico + troceo a 4096 | S2,S3 | inline | — |
| T4 | Poll loop: texto, documento, foto (con caption), `callback_query`; un único chat dueño | S3,S4 | inline | — |
| T5 | Vault: guardado de adjuntos entrantes en `inbox/`, notas markdown con frontmatter, nombres seguros, límites de tamaño | S4,S6 | inline | — |
| T6 | Bridge Pi: proceso persistente `pi --mode rpc`, sesión nombrada, streaming por drafts, timeout y recuperación | S1 | inline | — |
| T7 | Extensión de tools para Pi: `save_note`, `append_note`, `send_file`, `ask_options` (botones `callback_data` en el rich message + espera de callback) | S3,S4,S6,S7 | inline | — |
| T8 | Persona/prompt de sistema adaptado a fase 1 con reglas de formato y de opciones | S2,S3 | inline | — |
| T9 | Wiring en `cmd/alter`: bridge como única ruta de texto libre; retirar AgentFlow de la entrada | S1 | inline | — |
| T10 | Verificación: unitarios (render/chunking, cliente con httptest, vault con tmpdir, bridge con RPC falso) + end-to-end con Telegram falso | S1-S8 | inline | — |

## Log

**L1** (petición original, verbatim):

> vale, te explico la idea que tengo de alter simplificada. dado que es un proyecto personal y solo un poc al que quiero testear con uso diario quiero: un canal de comunicacion directo entre un gentle-shell completo y telegram. solo se enviaria la parte vervosa, sin codigo. tenemos que hacer que se puedan enviar mensajes ricos como lo haria desde una terminal con gentle shell, texto, markdown, tablas, preguntas, opciones, etc. yo tengo que poder enviar archivos y enlaces y gentle shell tambien. usariamos el vault global del server para almacenar todo, notas, tareas, listas y algunos archivos, aunque como tendremos acceso completo podemos usar otras carpetas para guardar archivos. esta seria la primera fase, la segunda seria crear un set de skills para casos concretos, como cuando se envia un enlace, un archivo o tarea/recordatorio. skills sencillas cortas y faciles de implementar y ejecutar. por ultimo en la fase 3, serian rutinas. esto quizas tambien puede ser una skill, la cual sabria que si le pido a gentle shell, recuerdame X tal dia, o cada mañana hazme un resumen de mis tareas, la skill indicaria usar un chron job o deamon para realizarlo (quizas podemos descargar algun util para despues poder listarlo). la idea seria que estos cronjobs o deamon despierten a gentle shell con un prompt creado por el mismo para buscar, ejecutar y notificarme por telegram. ahora vamos a por la fase 1. creo que con crear un canal flexible y completo entre gentle shell y telgram seria un buen inicio, pero megustaria saber si es facil instalar el server que se comnica con la api de telegram para no tener el limite de peso de un archivo, o no es viable usando solo tailscale

**L2** (decisión de base): "Bridge como referencia, reescribir" — se reescribe en
`minimal` siguiendo el patrón de `origin/bridge` sin heredar sus decisiones.

**L3** (decisión de vault): localizar el vault existente, no inventarlo.
Localizado `/home/balneario/vault` (sin `.obsidian`; 5 ficheros; ya contiene
`alter/ideas.md`, `alter/ia-utiles.md` y `scripts/save-ia-url.sh` con la
convención enlace→nota).

**L4** (decisión Bot API): "Después (recomendado)" — se difiere; base URL
configurable desde T1.

**L5** (evidencia de pi 1.4.0, `pi --help`): `--no-extensions` desactiva el
descubrimiento pero **las rutas explícitas `-e` siguen cargando**; existe
`--no-builtin-tools` para dejar solo tools de extensión; `--session-id` crea o
reanuda una sesión concreta. Esto resuelve la discrepancia §11 del design record
de `bridge` y habilita la carga limpia con extensión propia.

**L6** (decisión de formato): preguntado por la nueva API de rich messages, se
confirma como camino principal sobre el plan inicial de markdown→HTML. Evidencia
documental: Bot API 10.1 (2026-06-11) añade Rich Messages, `sendRichMessage` y
`sendRichMessageDraft`; 10.2 (2026-07-14) añade media y bloques de input; 10.3
añade tablas nativas. Límites: 32768 caracteres UTF-8, 500 bloques, 16 niveles
de anidamiento, 50 media, 20 columnas por tabla. Markdown GFM-compatible con HTML
arbitrario. Los media blocks solo aceptan HTTP/HTTPS, así que `sendDocument`
sigue siendo el transporte de archivos locales. **No verificado empíricamente en
esta sesión** (sin token ni salida a Telegram): la primera prueba real es T2.

**L7** (verificación independiente de T6, commit `d8f7e0a`). Tres defectos
reproducidos con probes propios, no opiniones:

1. **Sin `cmd.Wait`** → un hijo zombie y ~1,7 fds perdidos por cada timeout,
   reinicio y `Close`. En un daemon de larga vida eso crece sin límite.
2. **Deadline antes del mutex**: un segundo prompt encolado hereda el
   presupuesto ya consumido por el primero y, al expirar, mata el proceso sano
   que acaba de arrancar. Realista en T9 (el usuario manda otro mensaje
   mientras Pi piensa).
3. **`handled` devolvía el draft como texto final** y `text=""` con `err=nil`,
   que el caller enviaría a Telegram como mensaje vacío.

Hallazgo que condiciona T7: `--tools` **reemplaza** la selección completa de
Pi, no la amplía. Con el fallback silencioso a `read,grep,find,ls`, olvidar
`ALTER_BRIDGE_TOOLS` desactiva las tools de escritura sin error visible. T7
debe añadir explícitamente `save_note,append_note,send_file,ask_options` a la
allowlist y verificarlo.

No verificado en ninguna de las dos pasadas: ninguna ejecución real de
`pi --mode rpc` (sin red ni modelo en alcance). Todo el protocolo se apoya en
los docs instalados y en un fake RPC.

## Contrato de tools (T7)

El bridge levanta un servidor HTTP en `127.0.0.1:0` con un token efímero por
proceso. La extensión de Pi recibe la URL por `ALTER_TOOLS_URL` y el token por
`ALTER_TOOLS_TOKEN`, y registra cuatro tools con `pi.registerTool`.

| Tool | Args | Efecto |
|---|---|---|
| `save_note` | `{title, body, folder?, tags?}` | `vault.SaveNote`; devuelve la ruta relativa |
| `append_note` | `{note, line}` | `vault.AppendLine` |
| `send_file` | `{path, caption?}` | `sendDocument`/`sendPhoto` al chat dueño |
| `ask_options` | `{question, options: 2..4}` | envía botones `callback_data` y espera la pulsación |

Reglas:

- Toda ruta se resuelve contra el vault y se rechaza si escapa del root.
- El servidor exige el token en `X-Alter-Token`; sin él responde 403. Evita que
  cualquier proceso local use las tools del bridge.
- Error de negocio → 4xx con `{"error": "..."}`; la tool devuelve ese texto al
  modelo para que lo explique al usuario, sin reintentar a ciegas.
- `ask_options` espera como máximo 10 minutos y se cancela con el contexto del
  turno. Sin respuesta, devuelve "sin respuesta" en vez de colgar el turno.
- Las cuatro tools deben estar en `ALTER_BRIDGE_TOOLS`: `--tools` reemplaza la
  selección completa de Pi, no la amplía (hallazgo de L7).

**L8** (verificación independiente de T7, commit `d0979fa`). Un `FAIL`
bloqueante y tres advertencias:

1. **`save_note` escapa del vault por un symlink.** Con un directorio enlazado
   dentro del vault (`vault/inbox -> /mnt/data/inbox`) y un `folder` hijo que
   todavía no existe (`inbox/sub`), `EvalSymlinks` devuelve ENOENT,
   `resolvePath` cae al path sin resolver y `vault.SaveNote` solo comprueba
   léxicamente: la nota se escribe fuera del root y la ruta devuelta al modelo
   miente. Reproducido con un árbol temporal.
2. `send_file` sobre un directorio responde 500, cuando el contrato dice que un
   error de negocio es 4xx.
3. El comentario de `ask_options` sobre el deadline promete más de lo que
   garantiza: el límite depende de que el asker inyectado respete el contexto.
4. Tests que pasarían sin el comportamiento: el de timeout no fija
   `DefaultAskTimeout=10m`, el de token no prueba "autorización antes de
   routing", y falta el caso de carpeta a través de un directorio enlazado.

Decisión: se corrige en `internal/tools/server.go` (resolver sobre el ancestro
existente más cercano) y, por defensa en profundidad, en `internal/vault`
(`resolve` con la misma comprobación), porque el store es quien escribe.

## Límites y fase 1.5 (decisión de producto, 2026-10-29)

Ver `docs/alter-limites.md`. Alter es tu gentle-shell por Telegram: mismas
herramientas, mismo directorio (`$HOME`), misma configuración global de Pi, con
identidad Alter. Lo de solo lectura era una restricción heredada de
`origin/bridge` (D9) que contradecía el requisito original.

Correcciones al plan original:

**L9** — Retirada de la restricción de solo lectura. La allowlist de solo
lectura y la "carga limpia" (`--no-skills`, `--no-extensions`,
`--no-context-files`, `--no-themes`, `--no-prompt-templates`) se eliminan: no
son el canal que se pidió y dejan a Pi sin `bash`, `edit` ni `write`.

**L10** — Las aprobaciones no existen de serie en Pi. `-a/--approve` es confianza
en archivos de proyecto, no permiso por comando. Se implementan con el hook
`tool_call` (puede bloquear con `{block:true}`), `ctx.ui.confirm()` (llega como
`extension_ui_request` en RPC) y el bridge respondiendo con botones. Fail-closed.

**L11** — La identidad se impone por prompt. `before_agent_start` permite
reemplazar el prompt de sistema (`forceSystemPrompt`), así que cargar todo y ser
Alter es compatible; el orden exacto de inyección debe verificarse en ejecución.

**L12** — El fallo de "hola" no es del bridge: `alter.service` ejecuta
`/home/balneario/alter/bin/alter` compilado el 2026-10-16, cuyo texto de error
está compilado dentro (`strings` lo confirma). El bridge nunca se ha desplegado.

Tareas nuevas:

| ID | Tarea | Specs | Estado |
|---|---|---|---|
| T11 | Permisos y ámbito: allowlist completa, cwd `$HOME`, carga global, timeout configurable | S1,S6,L9 | inline | este commit |
| T12 | Identidad: persona con disciplina Gentleman y prompt efectivo verificado | L11,L13,L14 | este commit |
| T13 | Aprobaciones por botones: `tool_call` + `confirm` vía bridge, fail-closed | L10,L15 | este commit |
| T14 | Desplegar: parar el servicio viejo, compilar, configurar y probar "hola" real | L12 | pendiente |

**L13** (identidad, T12). Medido antes de diseñar, no supuesto:

- El handler de una extensión cargada con `-e` corre **antes** que el de
  `gentle-pi`, así que añadir texto a `appendSystemPrompt` nunca gana: es el
  mismo sitio donde `appendSystemPromptOnce` escribe después.
- Pi renderiza las `sections` propias **después** de `addendum`, skills y cwd,
  con independencia del orden de handlers. Por eso la identidad de Alter se
  inyecta como sección `alter_identity`: es una sección personalizada y queda
  tras el bloque del Gentleman.
- Verificación empírica con el `pi` instalado: la respuesta literal fue "Soy
  Alter, tu secretario personal: el mismo agente con otra voz, disponible por
  Telegram para leer, ejecutar y verificar lo que necesites."
- La identidad es el último **bloque de instrucciones**, no los últimos bytes:
  el catálogo de servidores MCP se añade después. Forzar el byte final exigiría
  interceptar el payload, rechazado por frágil.

**L14** (defecto propio, corregido). Al mover el cwd del hijo a `$HOME` (T11),
las rutas relativas de `-e` y `--append-system-prompt` dejaron de resolver: Pi
fallaba con "Extension path does not exist" y el prompt del persona se pasaba
como cadena literal. Ningún test lo detectaba porque el fake no resolvía rutas.
Se corrigen absolutizando ambos y pasando `ALTER_PERSONA_FILE` al hijo. La
lección: un cambio de entorno de ejecución necesita una aserción sobre la forma
de los argumentos, no solo sobre su presencia.

**L15** (aprobaciones, T13). El bucle de polling bloqueaba dentro de cada turno:
un botón pulsado mientras Pi trabajaba **no se podía recibir**, porque el mismo
bucle que debe leer el callback estaba esperando la respuesta del turno. Esto
afectaba ya a `ask_options`, no solo a las aprobaciones. Los turnos pasan a
ejecutarse fuera del bucle con `turnMu` (siguen serializados) y `Run` los espera
antes de cerrar. Es el cambio más ancho de la tarea y el que más merece revisión.

`--no-approve` se retira: las confirmaciones ya tienen a quién responder.
Política del gate: `bash`, `write` y `edit` siempre piden confirmación; `read`,
`grep`, `find`, `ls`, `save_note`, `append_note`, `send_file` y `ask_options`
nunca; cualquier otra herramienta (incluidas las de paquetes) también pregunta,
para que nada se cuele por no estar en la lista.

**L16** (verificación independiente de T13, commit `1da4653`). Fail-closed real,
slot compartido sin cruces y dispatch de turnos correcto (probado: cancelar con
una aprobación pendiente devuelve el control en 0 s, sin deadlock; tres mensajes
producen tres entregas; una pulsación durante el turno se consume). Tres
hallazgos que sí exigen trabajo:

1. **El preview del comando oculta la cola.** Se recorta a 240/280 caracteres, así
   que un `rm -rf` al final de un comando largo no se ve. La aprobación dejaría de
   informar justo cuando más importa.
2. **`turns.Wait()` y `turnMu` no tienen test.** Quitarlos deja la suite entera en
   verde: las dos garantías estructurales de L15 no están ancladas.
3. **Advisories**: un id vacío produce una respuesta que Pi no puede casar (el turno
   muere por timeout, pero cierra); un handler que ignore el contexto puede exceder
   el deadline; y si la extensión no carga, el gate desaparece sin más que un log.

Ninguno bloquea el código de T13; los tres se cierran antes de T14.
