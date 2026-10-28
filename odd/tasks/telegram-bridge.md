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
citas, listas, enlaces), **markdown**, **tablas**, **preguntas** y **opciones**
seleccionables. Debe sentirse como una terminal con Pi, no como un chat plano.

**S4 — Archivos en ambos sentidos.** El dueño puede enviar archivos por Telegram
y Pi recibirlos; Pi puede enviar archivos al dueño.

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
20 MB descarga) se acepta en esta fase.

## Tasks

| ID | Tarea | Specs | Ruta | Commit |
|---|---|---|---|---|
| T1 | Config: `ALTER_TELEGRAM_API_BASE`, `ALTER_VAULT_DIR`, `ALTER_BRIDGE_*`, carga de `.env` | S1,S6,S8 | inline | — |
| T2 | Cliente Bot API: sendMessage HTML, sendDocument por ruta, sendPhoto, getFile+descarga, sendChatAction, sendMessageDraft, inline keyboards, answerCallbackQuery | S3,S4,S5,S8 | inline | — |
| T3 | Render markdown→HTML de Telegram, tablas→`<pre>`, troceo a 4096 sin romper bloques ni entidades | S2,S3 | inline | — |
| T4 | Poll loop: texto, documento, foto (con caption), `callback_query`; un único chat dueño | S3,S4 | inline | — |
| T5 | Vault: guardado de adjuntos entrantes en `inbox/`, notas markdown con frontmatter, nombres seguros, límites de tamaño | S4,S6 | inline | — |
| T6 | Bridge Pi: proceso persistente `pi --mode rpc`, sesión nombrada, streaming por drafts, timeout y recuperación | S1 | inline | — |
| T7 | Extensión de tools para Pi: `save_note`, `append_note`, `send_file`, `ask_options` (keyboard + espera de callback) | S3,S4,S6,S7 | inline | — |
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
