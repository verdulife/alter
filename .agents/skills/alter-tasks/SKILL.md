---
name: alter-tasks
description: Procedimiento para apuntar una tarea, un recordatorio o un evento, cerrarlo después y contestar «¿qué tengo pendiente?». Úsala con «recuérdame…», «apunta…», «tengo que…», «el martes tengo…» o cuando haya que marcar algo como hecho. El formato de la entrada lo manda alter-vault.
---

# Tareas, recordatorios y eventos

Qué carpeta, qué claves y qué valores: `alter-vault`. Aquí, qué hacer con la
frase que llega.

## Qué tipo

| El Dueño dice | Se crea |
|---|---|
| «recuérdame llamar a mamá el martes a las siete» | `reminder` con `remind_at` |
| «apunta: renovar el DNI», «tengo que pedir cita» | `task`, con `due` si da fecha límite |
| «el martes tengo dentista», «el 3 me voy a Bilbao» | `event` con `due` |
| «todos los martes al gimnasio» | el tipo que toque, con `recurrence: weekly` |

Ante la duda entre tarea y nota, `alter-vault` ya lo resuelve: nota.

## Fecha: resolver o preguntar

Resuelve la fecha en el formato exacto de `alter-vault` (hora de Madrid) y
confírmala en lenguaje normal («martes 3 de noviembre, 19:00»), nunca en RFC3339.

Pregunta en lugar de inventar cuando:

- **El recordatorio no tiene fecha resoluble** («recuérdame llamar a mamá»): sin
  día y hora no hay recordatorio válido. Pregunta una sola cosa: cuándo.
- **«El martes» puede ser dos martes**: si no está claro si es esta semana o la
  siguiente, no elijas. Pregunta con `ask_options` y las dos fechas concretas.
- **Falta la hora de un recordatorio**: propone una (09:00 si es del día, 19:00
  si es de después del trabajo) y que la confirme.

En un `event` sin hora, guarda a las 09:00 y dilo en la confirmación: el evento
existe igual y la hora exacta rara vez importa.

## Cerrar

Se edita el fichero: `status: done` (hecha) o `status: cancelled` (descartada), y
una línea en el cuerpo con la fecha si aporta. Nunca se borra, nunca se renombra,
nunca se crea un segundo fichero para lo mismo. Reabrir es volver a `open`.

## «¿Qué tengo pendiente?»

Las recetas de búsqueda están en `alter-vault`; no las repitas aquí. El orden de
la respuesta: primero lo atrasado y lo de hoy, después lo que no tiene fecha, y
lo cerrado solo si lo pide. Título y fecha, sin rutas ni nombres de fichero.

## Nada dispara estos recordatorios

**Hoy no hay ningún proceso que lea `remind_at`: el recordatorio se guarda bien, pero no llega ningún aviso.** No prometas una notificación. Si el Dueño cuenta con que le avises, dilo en una línea y ofrece lo que sí puedes hacer: dejarlo apuntado para que lo vea, o un `event` en la fecha.

Al terminar, una línea: qué has apuntado, de qué tipo y cuándo.
