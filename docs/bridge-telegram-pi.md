# Bridge — Telegram → Pi directo

> Estado del documento: **borrador de diseño (M0).** Viviente: se actualiza a
> medida que se deciden y validan los hitos. Ruta de trabajo: rama `bridge`.

## 1. Contexto y objetivo

`alter` es un secretario personal local. Hoy el runtime tiene un **orquestador
propio** (Orchestrator → AgentFlow → CapabilityFlow) que dirige las
capacidades y el scheduling, y un adapter de Pi **one-shot** (`pi --mode rpc
--no-session`) que no conserva contexto entre mensajes.

Propuesta acordada: **sustituir el orquestador propio por Pi agent como
orquestador.** La comunicación pasa a ser **directa Telegram → Pi**, y Pi
dispone de **tools deterministas** (solo esas) para crear, editar, listar,
completar o cancelar datos. Pi **nunca** toca la DB directamente: opera
exclusivamente a través de las tools deterministas.

El objetivo de esta rama no es `alter` completo: es ir **por hitos pequeños**,
empezando por el canal de comunicación Telegram ↔ Pi con sesión persistente.

## 2. Decisiones tomadas

| # | Decisión | Estado |
|---|---|---|
| D1 | Cambio "importante": rama nueva `bridge` desde `master`. | ✅ hecho |
| D2 | Dirección: Pi como orquestador, descartando el orquestador propio. | ✅ acordado |
| D3 | Se conserva: **DB** (storage/sqlite + ports de dominio) y **tools deterministas** (capabilities). | ✅ acordado |
| D4 | Pi solo usa las tools deterministas (allowlist), no edita directo. | ✅ acordado (principio) |
| D5 | No se borra código viejo en la rama todavía; se retira al final si la vía nueva triunfa. | ✅ acordado |
| D6 | Persistencia de sesión: mecanismo concreto **pendiente** (ver §5). | ⏳ abierta |
| D7 | **Un solo chat / un solo usuario** (el dueño). No existe concurrencia entre chats. | ✅ confirmado |
| D8 | **El bridge es la única ruta de entrada de texto libre.** El natural handler AgentFlow queda retirado del camino de entrada en `cmd/alter/main.go`: con `ALTER_PI_BRIDGE` desactivado el texto libre queda deshabilitado (nunca cae de nuevo en AgentFlow). AgentFlow se conserva solo para el Orchestrator del Scheduler (notificaciones) hasta su retirada total en M5. | ✅ hecho |
| D9 | **Permisos de pi en el bridge: solo lectura.** Allowlist estricta `--tools read,grep,find,ls` por defecto (`ALTER_BRIDGE_TOOLS`). Se descartan `bash`/`edit`/`write` (ejecución/escritura en el servidor). Las tools deterministas (grupo C) se añadirán a la misma allowlist en M2. | ✅ hecho |
| D10 | **Personalidad en archivo .md.** Uno o varios markdowns se inyectan al system prompt vía `--append-system-prompt` (`ALTER_BRIDGE_PROMPT_FILES`, separados por `;`). `alter-persona.md` define al secretario virtual (español de España, conciso, sin voseo) y declara explícitamente sus capacidades (solo lectura + conversación) para que ante peticiones fuera de alcance responda en una frase sin razonar ni intentar rodeos. No se necesita trust de proyecto: `--no-context-files`/`--no-approve` se mantienen. | ✅ hecho |

## 3. Inventario que se conserva

### 3.1 DB (núcleo, intacta)

- `internal/domain` — entidades y puertos: `Task`, `Trigger`, `Event`,
  repositorios.
- `internal/storage/sqlite` — implementación SQLite (store, task_repo,
  trigger_repo, event_store, migrations 0001–0004).

### 3.2 Tools deterministas (capabilities)

Ya tienen la forma correcta: nombre + descripción + **JSON Schema** de args y
un handler concreto. Son exactamente el molde de una tool de Pi.

| Capability | Descripción | Schema (args) |
|---|---|---|
| `list_tasks` | List all tasks | `{}` |
| `create_task` | Create a new task | `{title: string(required), description?, priority?, due_at?}` |
| `complete_task` | Complete an existing task by textual reference | `{task_ref: string(required)}` |
| `cancel_task` | Cancel an existing task by textual reference | `{task_ref: string(required)}` |
| `create_reminder` | Create a reminder (relative, absolute o recurrente) | `{title: string(required), relative?, absolute_time?, absolute_date?, recurrence?}` |

Fuente de verdad: `internal/capability/runtime.go`
(`RegisterShippedCapabilities`). Estas capabilities **se reutilizan tal cual**
como capa de ejecución; no se descartan.

## 4. Arquitectura objetivo

```
Telegram (entrante)
   │  texto libre
   ▼
Bridge (módulo nuevo: reusa canal/client Telegram)
   │  "prompt" + manejo de sesión
   ▼
Pi agent --modo RPC  (proceso persistente, sesión guardada, carga limpia)
   │  decide qué tool usar
   ▼
Tools deterministas (extensión pi: registerTool)
   │  executa la capability correspondiente
   ▼
DB (SQLite)
   │  resultado → vuelve como texto de la tool → respuesta Pi
   ▼
Telegram (saliente) → usuario
```

### 4.1 Qué se reutiliza

- **Canal/Cliente Telegram** (`internal/adapter/telegram/channel.go`,
  `client.go`): transporte de salida y cliente de la API. Se reutilizan, no se
  reescriben.
- **Tools deterministas** (§3.2) y **DB** (§3.1).

### 4.2 Qué se descarta (solo al final, D5)

- `internal/adapter/agent/orchestrator.go` (composite propio).
- `internal/application/agentflow.go`, `capabilityflow.go`.
- `internal/capability/plan*.go`, `dispatcher.go`, `plannercontext.go` (el
  planner propio pasa a ser innecesario: Pi planifica solo).
- `internal/scheduler` (si el orquestador propio se retira).

> **Regla:** nada se borra hasta que la vía nueva Telegram→Pi esté validada de
> punta a punta (hitos M1–M3).
>
> **Simplicidad clave (D7):** solo existe un chat de Telegram de un único
> usuario (el dueño). No hay concurrencia entre chats, así que la sesión y el
> ciclo de vida son de **un solo flujo**; no hace falta diseño multi-tenant.

## 5. Persistencia de sesión (decisión D6, abierta)

El adapter actual espwna un proceso **efímero por ejecución** (`--no-session`),
así que **no hay contexto entre mensajes**. Para el objetivo hay que persistir
el hilo. Candidatos:

- **(A) Proceso Pi persistente por chat.** Un proceso `pi --mode rpc` vivo por
  conversación; cada mensaje nuevo usa `prompt`/continuación del mismo hilo.
  Conserva el contexto en memoria del proceso. Reto: gestión de vida, timeouts
  y concurrencia entre chats.
- **(B) Sesión nombrada por chat.** Cada mensaje vuelve a invocar `pi` pero
  **sin** `--no-session`, usando un ID de sesión derivado del chat de Telegram
  para reanudar el mismo hilo desde el fichero de sesión. Reto: coste de
  reinicio por mensaje y correcto mapeo chat→sesión.

**Recomendación para el M1 (spike):** probar **(A)**, un proceso persistente por
chat, porque es lo que mejor refleja "comunicación directa con Pi" y el modelo
de continuidad. Al ser **un único chat (D7)**, el proceso persistente es de un
solo flujo y no requiere gestión multi-chat; B no es necesario salvo que el
spike revele otro problema.

## 6. Carga limpia de Pi

Alineado con la discusión previa sobre "sin cargar nada extra". Cuando se lance
Pi (ya sea A o B), la consigna es:

- **Mantener** la sesión → **sin** `--no-session`.
- **Desactivar** descubrimiento automático: `--no-skills --no-prompt-templates
  --no-themes --no-context-files` (y `-na` para ignorar archivos de proyecto).
- **Allowlist de tools solo lectura** por defecto: `--tools read,grep,find,ls`
  (D9). `bash`, `edit` y `write` quedan fuera: pi no ejecuta ni escribe en el
  servidor. En M2 la allowlist se amplía con los nombres de las tools
  deterministas.
- **Personalidad** (D10): `--append-system-prompt <archivo.md>` por cada
  fichero de `ALTER_BRIDGE_PROMPT_FILES`. Pi resuelve la ruta y lee el
  contenido; no depende del trust de proyecto.
- `--no-extensions` **NO** se aplica en esta rama: las tools deterministas se
  entregan como extensión (los adapters de la rama de hoy — engram, gentle-pi —
  son ajenos a este cambio y no se cargan en el bridge).

## 7. Entrega de las tools deterministas a Pi

Opción recomendada **((a))** frente a exponer un bash CLI:

- Implementar una **extensión de Pi (TypeScript/Node)** que registra cada tool
  con `pi.registerTool({ name, description, parameters, execute })`.
- Cada `execute` invoca la lógica determinista correspondiente. Dos sub-vías:
  - **(a1)** llamar a un CLI Go (el binario `alter` expone subcomandos
    deterministas por tool) vía subprocess.
  - **(a2)** que la extensión hable con un pequeño servicio/proceso Go que
    ejecuta la capability contra la DB.
- El `parameters` de la tool reproduce el JSON Schema de la capability (§3.2),
  que ya está definido.

Decisión (a1 vs a2): **abierta**; se resuelve en M2 con datos reales
(contrato de entrada/salida, coste de arranque, acoplamiento).

## 8. Aseguramiento del determinismo

No se depende de "que Pi se porte bien":

- **Allowlist**: con `--tools <lista>` Pi **solo** puede invocar las tools
  deterministas. No tiene `bash`, `write`, `edit` ni `read` genéricos.
- Las tools deterministas tienen **schema estricto** de args y **salida
  tipada/estable** (p. ej. `TaskView` en `list_tasks`).
- Pi no toca la DB: la única vía de mutación son las tools.

## 9. Hitos

| Hito | Alcance | Criterio de salida |
|---|---|---|
| **M0** | Este documento + rama `bridge`. | Doc aprobado por Verdu. |
| **M1** | Canal Telegram→Pi con **sesión persistente**, sin tools aún (spike). Solo charla continua desde Telegram. | Mensajes encadenados en el mismo hilo desde Telegram, sin DB. |
| **M1.5** | **Streaming** de respuesta: typing (`sendChatAction`) + draft animado (`sendMessageDraft`) mientras pi genera, finalizando con `sendMessage`. | La respuesta se ve escribir carácter a carácter en Telegram y se persiste al final. |
| **M2** | **Primera tool determinista** como extensión + allowlist (p. ej. `list_tasks`). | Desde Telegram, Pi *llama* la tool y devuelve el resultado; se verifica que no edita directo. |
| **M2-W** | **Web access** en el bridge: `web_search` + `fetch_content` (ver §12). | Desde Telegram, Pi busca en la web y devuelve resultados citados; permisos y providers decididos (D-W). |
| **M3** | Ampliar a `create_task`, `complete_task`, `cancel_task`, `create_reminder`; afinar schemas. | Operaciones CRUD vía Pi por Telegram. |
| **M4** | Ciclo de vida del proceso único: reconexión/resuperación, timeouts, reinicio limpio tras caída. | El puente se recupera solo tras caída de Pi/red, sin servicio manual. |
| **M5** | Limpieza: retirar orquestador/capabilities/scheduler del antiguo camino si ya no se usan. | Solo con validación previa (D5). |

## 10. Decisiones abiertas

- D6: mecanismo de persistencia de sesión (A vs B) → **M1**.
- §7: integración de tools (a1 vs a2) → **M2**.
- D5: momento y alcance de la retirada del código viejo → **M5**.
- D-W: alcance y proveedor de web access en el bridge → **M2-W** (§12).

## 11. Pendientes previos de arranque (orden del árbol)

- Añadir `.codegraph/` a `.gitignore` (índice local, no debería estar en git). ✅
- Decidir qué hacer con `cmd/alter/main_test.go` (actualmente sin commitear). ✅ retirado del árbol
- **Discrepancia detectada (2025-09-18):** §6 y D9 dicen que `--no-extensions` NO se aplica, pero el código real de `internal/bridge/bridge.go` (`args()`) **sí** pasa `--no-extensions` junto a `--no-skills`/`--no-prompt-templates`/etc. La "carga limpia" actual bloquea la auto-descarga de extensiones. Impacta directamente M2 y M2-W: resolver antes de añadir ninguna tool como extensión.

## 12. Web access (búsqueda y fetch) desde Telegram — D-W

> Contexto (2025-09-18): el agente de Pi ya dispone de capacidades web en **esta
> máquina** vía el paquete npm `pi-web-access`
> (github.com/nicobailon/pi-web-access), listado en
> `~/.pi/agent/settings.json` (`"packages": [ ... "npm:pi-web-access" ... ]`).
> Es un **proveedor externo**, no una tool del core de pi: se inyecta como
> extensión (TS/Node) que registra tools con `pi.registerTool`. Es exactamente el
> mecanismo (a-extension) previsto en §7.

### 12.1 Qué registra (inventario real)

`pi-web-access` registra **4 tools**, todas con nombre configurable (config
`toolNames`) y cada una deshabilitable de forma independiente (config
`tools.<clave>.enabled`):

| Tool (nombre por defecto) | Clave en config | Qué hace | Requiere red |
|---|---|---|---|
| `web_search` | `webSearch` | Busca en la web vía provider (SSRF-guardado), con backend de síntesis + fuentes citadas. | Sí (saliente) |
| `fetch_content` | `fetchContent` | Extrae contenido legible de URLs (incluye PDF/YouTube). | Sí (saliente) |
| `source_check` | `sourceCheck` | Verifica una afirmación contra la web con citas por pasaje. | Sí (saliente) |
| `get_search_content` | `getSearchContent` | Recupera contenido almacenado de una búsqueda previa (`responseId`). | No (local) |

Necesita **al menos un provider de búsqueda disponible**: una API key de un
buscador (OpenAI/Brave/Exa/Tavily/Jina/Kagi/Perplexity/Gemini/…) o un **SearXNG
local** (preferido por defecto en `pi-web-access`). El provider elegido vive en
un config propio del paquete (`getWebSearchConfigPath()`), **no** en las env de
alter.

### 12.2 El conflicto con la "carga limpia" (y D9/D6)

El bridge hoy lanza pi con `--no-extensions` (§11 discrepança), lo que **apaga
la extensión** que registra `web_search`/`fetch_content`. Además, aunque se
cargara, la allowlist `--tools read,grep,find,ls` (D9) **no** haría visible
ninguna tool web. Para habilitar web access en el bridge hacen falta **dos
cambios** en `internal/bridge/bridge.go`:

1. **Cargar la extensión** (de forma controlada), y
2. **Añadir los nombres** de las tools web elegidas a `ALTER_BRIDGE_TOOLS`.

### 12.3 Vías de carga de la extensión (decisión D-W, abierta)

- **(w1) Vía paquete global de pi.** El puente hereda la auto-discovery de
  paquetes del host (`~/.pi/agent/settings.json`) quitando `--no-extensions`.
  Es la más barata, pero **rompe la carga limpia**: carga *todo* (gentle-pi,
  gentle-engram, pi-btw…), no solo web access. Contradice D9/§6.
- **(w2) Extensión explícita del puente.** `--extensions <ruta del index.ts>`
  apuntando solo al `pi-web-access` (o a un stub propio que re-exporte sus
  tools), manteniendo el resto de `--no-*` intactos. Conserva la carga limpia
  (solo se carga web access). Es la vía recomendada.
  - Sub-vía **(w2a)**: la extensión del puente instala/importa `pi-web-access`
    con npm de forma local al proyecto y expone las tools web.
  - Sub-vía **(w2b)**: `--extensions` apunta directo a la ruta absoluta del
    index del paquete global ya instalado.
- **(w3) No exponer en el bridge.** Mantener web access solo en sesiones
  locales de pi (las de esta máquina), no en Telegram.

Se debe confirmar además qué flag real de pi `--extensions` acepta una **lista**
(múltiples archivos) y cómo se comporta con `--no-extensions` (si son mutuamente
-excluyentes o si el archivo explícito tiene prioridad).

### 12.4 Límite de alcance de tools web (D-W)

Recomendación: **permitir solo `web_search` + `fetch_content`** (y, opcionalmente,
`get_search_content` para recuperar resultados sin volver a buscar). **Descartar
`source_check`** por defecto durante el primer despliegue: requiere estatura de
citas por pasaje y no es necesario para la interacción Telegram diaria.
Aplicable con la config propia del paquete (`tools.sourceCheck.enabled: false`),
sin tocar código del bridge.

Con la allowlist de tools deterministas, la lista `ALTER_BRIDGE_TOOLS` quedaría
algo así (cuando M2 añada las tools de tareas):

```
read,grep,find,ls,list_tasks,web_search,fetch_content,get_search_content
```

(O los nombres que se decidan; `fetch_content` y `get_search_content` dependen
el uno del otro: si se registra el primero, tiene sentido exponer el segundo.)

### 12.5 Seguridad y consecuencias (para decidir, no ignorar)

- Web access **no es "lectura del servidor local"** (D9): son **llamadas de red
  salientes** y, si se usa un buscador con API, **credenciales del host** keys
  en el config del paquete). Debe tratarse como una categoría de permisos nueva,
  no como un default-ampliable.
- El mundo de determinismo (§8) aplica igual a estas tools: pi llama por
  allowlist, no ejecuta `bash`; pero el *efecto* (fetch de una URL arbitraria)
  es externo. `pi-web-access` ya incluye **protección SSRF** y saneo de
  credenciales, lo que mitiga el abuso local pero **no** elimina el coste de
  red ni el riesgo de fugas de data en resultados.
- D7 (un solo usuario/chat): se mantiene; la concurrencia sigue sin existir.

### 12.6 Esbozo de implementación (M2-W)

1. **Confirmar flags reales de pi** para carga de extensión explícita
   (`--extensions`), y la interacción con `--no-skills`/`--no-context-files`.
2. **Decidir D-W (w1/w2/w3)** y el proveedor de búsqueda (¿SearXNG local o API
   key existente?).
3. **`internal/config`**: añadir `BridgeExtensions` (lista de rutas) y
   `BridgeWebTools` (bool), con defaults que mantengan la carga limpia. Env
   `ALTER_BRIDGE_EXTENSIONS` / `ALTER_BRIDGE_WEB_TOOLS`.
4. **`internal/bridge/bridge.go`**: en `args()`, sustituir `--no-extensions` por
   `--extensions <ruta>` cuando esté configurado, y ampliar la allowlist con las
   tools web cuando `BridgeWebTools` sea true.
5. **Config del provider de búsqueda** (fuera de alter): fijar la API key o
   SearXNG en el config del paquete; documentarlo en `README`/convención de
   despliegue.
6. **Test**: con un proveedor headless (p. ej. stub de `--extensions`), verificar
   que `web_search` se llama por allowlist y devuelve resultados citados sin que
   pi toque la DB.
7. **Designar criterio de salida M2-W**: desde Telegram, pedir a pi algo que
   requiera web ("busca X") y ver el resultado citado.

Pendiente de resolver en la siguiente pasada st": la numeración interna del
paquete pi-web-access (config) y si su `index.ts` puede importarse vía
`--extensions` sin entorno de TUI disponible. Para ello conviene revisar su
`README.md` y el flag `--extensions` de pi antes de fijar w2.
