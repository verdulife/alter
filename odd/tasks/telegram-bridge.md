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

**S7 — Pi no escribe.** Pi sigue sin `bash`, `edit` ni `write`: lee el vault con
las tools nativas de solo lectura (`read`, `grep`, `find`, `ls`) ejecutando con
`cwd` = vault, y **toda escritura pasa por tools deterministas** expuestas por el
bridge (`save_note`, `append_note`, `send_file`, `ask_options`).
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
| 3 | T3 (salida rica + fallback) | siguiente |

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
