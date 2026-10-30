# Fase 3 — Recordatorios que suenan

> Plan de diseño. Nada de esto está implementado todavía. El objetivo es cerrar
> la única promesa que el sistema hace hoy y no cumple: un recordatorio con
> `remind_at` se guarda bien y nunca avisa.

## Principio

**Sin infraestructura nueva.** Nada de cron, nada de daemon aparte, nada de
segunda conexión a Telegram. El bridge ya está vivo, ya es dueño del token y ya
tiene una sesión de Pi con contexto. La fase 3 es un ticker dentro de él.

```
alter.service (ya existe)
  └── bridge
       ├── poller           ← tus mensajes
       ├── turnos           ← lo que ya hay
       └── ticker (60 s)    ← nuevo, y es todo
```

## Por qué un ticker y no un cron

| | Cron externo | Ticker interno |
|---|---|---|
| Proceso nuevo | sí | **no** |
| Compite por el token | tendría que hablar con el bridge | **no** |
| Sesión de Pi | una segunda, sin contexto | **la misma** |
| Apagado | dos ciclos de vida | uno |
| Recuperación al volver | la tiene que inventar | trivial: mira los vencidos |

El ticker es ~80 líneas contra un subsistema.

## Flujo de un disparo

```
cada 60 s
  due := recordatorios con status=open y remind_at <= ahora, sin last_fired
  si vacío → nada (no se registra, no se loguea)
  marcar last_fired en cada uno (escritura atómica, ANTES de notificar)
  si son varios → un solo turno con todos, no N mensajes
  runTurn("Recordatorios vencidos: …")
  Pi redacta el aviso; el bridge lo entrega como cualquier respuesta
```

**Marcar antes de notificar** es la decisión de idempotencia: si el envío
falla, se pierde un aviso (se ve en el log) en lugar de repetirse cada minuto
para siempre. Se documenta como riesgo aceptado; nunca hay duplicados.

## Contrato de datos

Ya existe y no se toca: `.agents/skills/alter-vault` define las claves, y
`internal/vault/schema.go` las valida. El ticker **rechaza** una entrada inválida
y la reporta en el log; no adivina fechas ni completa campos.

Campos que el ticker usa y escribe:

| Campo | Lectura | Escritura |
|---|---|---|
| `remind_at` | filtro por vencimiento | — |
| `status` | solo `open` | — |
| `last_fired` | si es posterior a `remind_at`, ya está hecho | timestamp RFC3339 |
| `recurrence` | `daily`/`weekly`/`monthly`/`yearly` | recalcula `remind_at` |

Un recordatorio **recurrente** no se marca como disparado: se adelanta su
`remind_at` a la siguiente ocurrencia. Los demás escriben `last_fired`.

## Configuración

| Variable | Default | Qué hace |
|---|---|---|
| `ALTER_REMINDERS_ENABLED` | `true` | apaga el ticker sin tocar nada más |
| `ALTER_REMINDERS_INTERVAL` | `60s` | frecuencia de comprobación |

Nada más. Sin fichero de configuración nuevo.

## Tareas

| ID | Tarea | Criterio |
|---|---|---|
| F3-1 | `internal/vault/reminders.go`: `DueReminders`, `MarkFired`, `NextOccurrence`, escritura atómica | tests de vencido, futuro, ya disparado, inválido, recurrente, zona horaria |
| F3-2 | Ticker en el bridge: arranque, parada con el contexto, un turno agrupado | test: un vencido produce un turno; ninguno produce cero |
| F3-3 | Config y documentación | variables en `docs/bridge-fase1.md` |
| F3-4 | Verificación real en el móvil | ver el criterio de aceptación |

## Criterio de aceptación

1. Crear un recordatorio con `remind_at` a dos minutos vista.
2. **Reiniciar el servicio** antes de que venza.
3. Recibir el aviso en Telegram, redactado por Pi.
4. Reiniciar otra vez: **no** se repite.
5. Un recordatorio con fecha pasada de hace una hora, creado mientras el
   servicio estaba parado, se dispara al arrancar (puesta al día).

Ese último punto es el que separa "funciona" de "funciona cuando todo va bien".

## No objetivos

- Gestionar recordatorios por comando (`/recordar`): se siguen creando
  hablando con el agente, que es lo que ya funciona.
- Notificaciones fuera de Telegram.
- Reintentos con backoff: si un aviso falla, se registra y se sigue.
- Precisión al segundo: un minuto de margen es aceptable para un secretario.

## Riesgos aceptados

- **Servicio caído en el momento exacto:** el aviso llega tarde, al arrancar.
  Con la puesta al día, no se pierde.
- **Reloj del sistema:** un salto de hora puede adelantar o retrasar un aviso.
- **Marcar antes de notificar:** un fallo de envío pierde ese aviso. Se prefiere
  a duplicarlo cada minuto.
- **Un solo proceso:** systemd garantiza una instancia; dos bridges con el mismo
  token ya se rompen hoy por `409 Conflict`, así que no es un riesgo nuevo.
