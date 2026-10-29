---
name: alter-vault
description: Contrato del vault de Alter. Crea, busca y cambia tareas, recordatorios, eventos y notas como markdown con frontmatter YAML plano. Úsala siempre que haya que apuntar algo, recordar algo, consultar qué está pendiente, listar por fecha o cambiar el estado de una entrada.
---

# Contrato del vault

El vault es `$HOME/vault`. Todo lo que Alter recuerda entre conversaciones vive
ahí, en ficheros markdown que un shell puede leer sin más. Esta skill es el
contrato: si la sigues, el fichero es correcto a la primera y otra sesión lo
encuentra sin preguntar.

Trabajas desde el repositorio de Alter, no desde el vault, así que usa siempre la
ruta absoluta `$HOME/vault/...`. Crear la carpeta y el fichero con `write` es
suficiente; no hay ninguna tool de vault.

## Las cuatro carpetas

| Carpeta | `type` | Qué guarda |
|---|---|---|
| `$HOME/vault/tasks/` | `task` | Algo que hay que hacer y que termina: abierto → hecho o cancelado. |
| `$HOME/vault/reminders/` | `reminder` | Un aviso en un instante concreto. **Un recordatorio sin `remind_at` es inválido: no lo crees así.** |
| `$HOME/vault/events/` | `event` | Algo que ocurre en una fecha y no depende de nadie: una cita, un viaje, una entrega. |
| `$HOME/vault/notes/` | `note` | Todo lo demás: información, enlaces, listas, ideas, resúmenes. Sin estado que cambiar. |

Cómo elegir el tipo, en este orden:

1. ¿Hay que avisar en un momento concreto? → `reminder`.
2. ¿Es algo que ocurre en una fecha, sin que tú lo ejecutes? → `event`.
3. ¿Es algo que el Dueño tiene que hacer y puede quedar hecho? → `task`.
4. Si no encaja en ninguno → `note`. Ante la duda entre tarea y nota, elige
   `note`: una nota no promete un estado que nadie va a mantener.

Reglas de fichero:

- Una entrada por fichero. Nunca dos entradas en el mismo fichero.
- Nombre: `<YYYY-MM-DD>-<slug>.md`, con la fecha de `created` y un slug en
  minúsculas, números y guiones: `^[a-z0-9]+(-[a-z0-9]+)*$`, entre 1 y 60
  caracteres. Ejemplo: `2026-10-29-renovar-dni.md`.
- El nombre no cambia nunca después de crearlo, ni al cerrar la entrada.
- `inbox/` recibe archivos del Dueño; no es una carpeta de entradas.
- Nada de entradas en la raíz del vault ni en otras carpetas. Si la carpeta del
  tipo no existe, la creas con `mkdir -p`.

## Frontmatter: claves planas

La línea 1 es `---`, después el frontmatter, después otra línea `---` y después
el cuerpo. No hay nada antes de la primera línea.

| Clave | Valores admitidos | Formato |
|---|---|---|
| `id` | `<YYYY-MM-DD>-<slug>` | Sin comillas. Igual que el nombre del fichero sin `.md`. Único y estable. |
| `type` | `task` \| `reminder` \| `event` \| `note` | Sin comillas. |
| `title` | texto en una línea | Siempre entre comillas dobles; las `"` internas, como `\"`. |
| `status` | `open` \| `done` \| `cancelled` | Sin comillas. |
| `priority` | `low` \| `normal` \| `high` | Sin comillas. |
| `created` | fecha y hora | RFC3339 con desfase. |
| `due` | fecha y hora | RFC3339 con desfase. |
| `remind_at` | fecha y hora | RFC3339 con desfase. |
| `recurrence` | `daily` \| `weekly` \| `monthly` \| `yearly` | Sin comillas. Ausente = una sola vez. |
| `last_fired` | fecha y hora | RFC3339 con desfase. |
| `tags` | lista separada por comas, sin espacios | Siempre entre comillas dobles; `tags: ""` si no hay. |

Formato de fecha y hora, exacto: `2026-10-29T18:30:00+01:00`. RFC3339 con
desfase numérico de la zona del Dueño (`Europe/Madrid`): nunca `Z`, nunca sin
desfase, nunca solo la fecha, nunca fracciones de segundo. Usa
`date +%Y-%m-%dT%H:%M:%S%:z` para obtener el valor correcto.

Obligatoriedad por tipo. `R` obligatoria, `O` opcional, `—` no se usa:

| Clave | `task` | `reminder` | `event` | `note` |
|---|---|---|---|---|
| `id` | R | R | R | R |
| `type` | R | R | R | R |
| `title` | R | R | R | R |
| `created` | R | R | R | R |
| `tags` | R | R | R | R |
| `status` | R | R | R | O (`open` por defecto) |
| `priority` | O (`normal` por defecto) | O (`normal`) | O (`normal`) | O (`normal`) |
| `due` | O | — | R | — |
| `remind_at` | O | R | O | — |
| `recurrence` | — | O | O | — |
| `last_fired` | — | O | — | — |

Significado:

- `status: open` es «no terminado». `done` y `cancelled` son estados finales de
  una tarea o de un recordatorio no recurrente.
- `priority` solo ordena; no cambia nada más. `high` se usa con moderación.
- `due` en `task` es la fecha límite; en `event`, el comienzo del evento.
- `remind_at` es el instante del aviso. En una tarea o un evento, es un aviso
  previo; en un recordatorio, es el motivo de existir.
- `recurrence` repite el aviso desde `remind_at` (o desde `due` en un evento) con
  esa cadencia. `last_fired` guarda el último instante en que se avisó; se
  escribe al avisar, nunca al crear.
- `tags` en minúsculas, separadas por comas y sin espacios: `tags: "personal,dni"`.

El frontmatter tiene que seguir siendo plano, para que un shell lo parsee con
`grep` y `cut`:

- Nada de listas YAML con `- `, ni mapas anidados, ni sangría, ni `|` ni `>`.
- Nada de comillas simples, ni comillas dobles fuera de `title` y `tags`.
- Nada de claves que no estén en la tabla. Si falta un dato, se omite la clave
  opcional; no se inventa una nueva.

Incorrecto:

```yaml
tags:
  - personal
  - dni
```

Correcto:

```yaml
tags: "personal,dni"
```

## Cuerpo

El cuerpo es markdown libre, pero con una forma fija al principio: un `# ` con el
mismo texto que `title` y, debajo, lo que haga falta (contexto, pasos, enlaces,
listas). Detalles que no caben en el frontmatter van aquí, nunca en claves
nuevas. Ejemplo de cuerpo de una tarea:

```markdown
# Renovar el DNI

Pedir cita en la comisaría de la calle X. Llevar foto y el DNI caducado.
```

## Cómo crear una entrada

1. Elige el tipo con el orden de arriba.
2. Busca antes de crear: puede existir ya (`grep -ril '<palabra>' "$HOME/vault"`
   con `2>/dev/null`). Si existe, la editas; no creas otra.
3. Calcula `created` con `date +%Y-%m-%dT%H:%M:%S%:z` y compón `id` y nombre de
   fichero con la misma fecha y el slug.
4. `mkdir -p "$HOME/vault/<carpeta>"`.
5. Escribe el fichero completo con `write`, en una sola pasada.
6. Confirma al Dueño en una línea qué entrada y en qué carpeta has creado.

## Cómo encontrar entradas

No hay tool de consulta: el vault es texto y las búsquedas se hacen con `bash`.
Añade `2>/dev/null` para que un glob vacío no ensucie la salida.

```bash
# Pendientes de cualquier tipo
grep -l '^status: open$' "$HOME/vault"/*/*.md 2>/dev/null

# Por prefijo de fecha en el nombre (todo lo de octubre de 2026)
ls "$HOME/vault/events" 2>/dev/null | grep '^2026-10'

# Por clave de frontmatter y fecha (prefijo del instante)
grep -l '^due: 2026-10-29' "$HOME/vault/events"/*.md 2>/dev/null
grep -l '^remind_at: 2026-10-30' "$HOME/vault/reminders"/*.md 2>/dev/null

# Por texto en el título o en el cuerpo, sin distinguir mayúsculas
grep -ril 'dentista' "$HOME/vault" 2>/dev/null

# Por etiqueta
grep -l '^tags: ".*dni.*"' "$HOME/vault"/*/*.md 2>/dev/null

# Por prioridad alta
grep -l '^priority: high$' "$HOME/vault"/*/*.md 2>/dev/null

# Creadas o modificadas a partir de una fecha
find "$HOME/vault" -name '*.md' -newermt '2026-10-01' -print

# Una entrada concreta, cuando ya sabes el id
ls "$HOME/vault"/*/2026-10-29-renovar-dni.md 2>/dev/null
```

Ordena por lo que el Dueño preguntó: por `due` o `remind_at` si pregunta «qué
toca», por `created` si pregunta «qué apunté».

## Cómo cambiar el estado

Se edita el fichero. Nunca se duplica, nunca se renombra, nunca se copia a otra
carpeta.

- Cerrar una tarea: `status: done` (hecha) o `status: cancelled` (descartada), y
  una línea en el cuerpo con la fecha y el motivo si lo hay. Nada más.
- Reabrir: `status: open` otra vez, sin tocar `id`, nombre ni `created`.
- Avisar un recordatorio: escribir `last_fired` con el instante del aviso. Si no
  tiene `recurrence`, además `status: done`. Si la tiene, sigue `open`.
- Corregir un dato (título, fecha, prioridad): se edita la clave en su sitio. No
  se añade una clave nueva ni una segunda línea con el mismo nombre.
- Borrar no es una transición: si el Dueño quiere quitarla de en medio, se usa
  `status: cancelled`. Nunca borres un fichero del vault.

## Dos entradas completas

Ejemplos válidos y copiables. En un recordatorio, fíjate en que `remind_at`
existe siempre:

```markdown
---
id: 2026-10-29-renovar-dni
type: task
title: "Renovar el DNI"
status: open
priority: high
created: 2026-10-29T09:12:00+01:00
due: 2026-11-15T10:00:00+01:00
tags: "personal,documentos"
---

# Renovar el DNI

Pedir cita en la comisaría de la calle X. Llevar foto y el DNI caducado.
```

```markdown
---
id: 2026-10-29-llamar-a-mama
type: reminder
title: "Llamar a mamá"
status: open
priority: normal
created: 2026-10-29T09:15:00+01:00
remind_at: 2026-10-30T19:00:00+01:00
recurrence: weekly
tags: "familia"
---

# Llamar a mamá

Preguntar por la revisión del médico.
```
