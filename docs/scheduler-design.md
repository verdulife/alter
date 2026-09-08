# Scheduler — Diseño (V1, one-shot)

> Estado: **implementado (V1 one-shot)**. El Scheduler vive en `internal/scheduler/` y está
> funcional; las secciones describen su comportamiento efectivo. Siguen fuera de V1 las partes
> marcadas como futuras (Telegram, notificaciones, recurrencia, event bus).

## Contexto y premisas

- SQLite es la **fuente de verdad**: `Trigger.NextFireAt`, `LastFiredAt`, `RetryAt`, `Enabled` se persisten
  y `TriggerRepository.ListEnabled()` devuelve triggers habilitados.
- `CalculateNextFireAt(...)` y `ExecuteTrigger(...)` son **funciones puras** de dominio.
- V1 = triggers **one-shot**. No hay bus de eventos, no hay Telegram, no hay notificaciones aún.
- El Scheduler **no mantiene estado de planificación persistente en memoria**: cada ciclo relee SQLite.
- Todo se **recupera de SQLite** tras un reinicio del proceso.

---

## Arquitectura: `internal/scheduler/`

Paquete de **orquesta (inbound)**, no una port driven. Vive al lado de `internal/service/` y es
construido por `cmd/alter/main.go`. Coherente con la nota de `internal/domain/ports.go`.

### Responsabilidades

- Leer `ListEnabled()` desde SQLite (única fuente de planificación).
- **Armar** triggers sin `NextFireAt` vía `CalculateNextFireAt`.
- **Detectar** triggers due (`NextFireAt != nil && NextFireAt <= now`) y procesarlos **todos** antes de planificar.
- Coordinar el flujo `ExecuteTrigger → action → persist → event → recalcular deadline`.
- Esperar sin polling hacia el siguiente deadline (`time.Timer` de un solo uso por ciclo).
- Despertar/reescanear ante señales de `Wake()`.
- Aislar errores por trigger; distinguir terminales de transitorios.
- Shutdown limpio vía `context`.

### Ciclo de vida (un goroutine único, `Run(ctx)`)

```
Run(ctx):
  loop:
    enabled := TriggerRepository.ListEnabled(ctx)      // fuente de verdad (SQLite)
    now := time.Now()

    // FASE ARMAR — garantizar NextFireAt != nil (y persistirlo)
    for t in enabled where t.NextFireAt == nil:
        task := TaskRepository.GetByID(t.TaskID)
        next, err := CalculateNextFireAt(t, task)
        if err != nil: log & skip            // malformado: se queda sin armar, sin busy-loop
        else: t.NextFireAt = &next; TriggerRepository.Update(t)

    // FASE DISPARAR — procesar todos los due antes de planificar
    due := [t in enabled where t.NextFireAt != nil && !t.NextFireAt.After(now)]
    for t in due:
        handleOne(ctx, t)                     // error aislado por trigger

    // FASE PLANIFICAR — siguiente deadline futuro, sobre los triggers aún enabled.
    // Tras DISPARAR se relee ListEnabled si se persistió algún cambio. El deadline es el
    // primer instante futuro entre NextFireAt y RetryAt: un RetryAt futuro despierta el loop
    // exactamente cuando la reintento se vuelve elegible; un NextFireAt ya pasado queda
    // excluido (fue tratado en DISPARAR o está retenido por un RetryAt pendiente).
    nextDeadline := min(NextFireAt_futuro, RetryAt_futuro) over (enabled still-enabled)

    if nextDeadline existe:
        wait(nextDeadline)                    // select: timer / wake / ctx.Done
    else:
        wait on wake / ctx.Done               // sin deadline: no hay timer; solo wake o shutdown
```

### Clean shutdown

- El `wait` hace `select { case <-timer.C; case <-wake; case <-ctx.Done() }`.
- `main` llama `cancel()` y espera a que `Run(ctx)` retorne. `ctx.Done()` desbloquea cualquier espera.
- Un único goroutine: no hay fugas de goroutines ni timers pendientes tras el retorno.

---

## Armado de triggers (CalculateNextFireAt)

- Solo se calcula `NextFireAt` cuando es `nil` (FASE ARMAR).
- El armado persiste el `NextFireAt` calculado, de modo que no se repite en cada ciclo y la
  planificación se basa siempre en el estado de SQLite.
- Un trigger malformado cuyo armado falla **no se persiste** (`NextFireAt` queda `nil`):
  nunca es `due`, queda excluido del deadline y se corrige solo si se arregla el trigger.

---

## `TriggerAction` — obligatorio (opción preferida)

```go
// domain.TriggerAction (port driven, vecina de Channel)
type TriggerAction interface {
    Execute(ctx context.Context, trigger Trigger, task Task) error
}
```

- **Requerido en la construcción del Scheduler** (no nulo). El Scheduler **no se arranca** hasta
  que exista una implementación real (p. ej. un `TelegramAction` que envuelva `domain.Channel`).
- **Prohibido un no-op exitoso**: nunca se debe marcar un trigger como disparado si no hubo
  consecuencia real. Exigir la acción en el constructor elimina ese bug de raíz (no se necesitó
  `ErrActionUnavailable`).
- La acción se ejecuta entre `ExecuteTrigger` y la persistencia (ver orden más abajo).

**Inyección futura:** cuando llegue Telegram, un adaptador que implementa `TriggerAction` y usa
`domain.Channel` se inyecta en `main.go`. Abre además la puerta a una lista de acciones (notificar + acción de IA) sin tocar el ciclo.

---

## Orden de ejecución: ExecuteTrigger → action → persist → event

```
task := TaskRepository.GetByID(t.TaskID)        // si no existe → terminal (ver abajo)
executed, err := ExecuteTrigger(t, task, now)   // pura; valida due/executable
if err != nil: manejar según severidad (terminal vs transitoria); NO persistir

err := action.Execute(ctx, executed, task)      // inyectada
if err != nil: log; NO consumir; persistir backoff RetryAt = now + actionRetryDelay
    (deja LastFiredAt intacto; el trigger sigue due, pero el RetryAt lo retiene hasta que elapse)

TriggerRepository.Update(ctx, executed)         // persiste LastFiredAt, NextFireAt=nil, RetryAt=nil, Enabled=false
EventStore.Save(ctx, EventTriggerFired{...})    // best-effort, nunca bloquea el fire
// recalcular deadline al terminar de procesar todos los due
```

- `LastFiredAt` / `NextFireAt` / `RetryAt` / `Enabled` se persisten **juntos en un único `Update`**
  con el `executed` devuelto por `ExecuteTrigger` (atómico a nivel de fila).
- La persistencia ocurre **después** de la acción exitosa: si la acción falla, el trigger no se
  consume; se persiste un backoff `RetryAt` (now + delay, por defecto 30s) que lo retiene hasta
  que elapse y luego reintenta. Nunca se marca «disparado» sin haber notificado.

### Semántica at-least-once

- Ventana crash entre `action` OK y `persist`: al recuperar desde SQLite el trigger sigue due y
  **se re-dispara** → posible **duplicado** de notificación.
- Para notificaciones es preferible un posible duplicado a perder una. Aceptado y documentado.
- `EventTriggerFired` va **al final del path de éxito**, exclusivamente tras persistir el fire.

---

## Detección de triggers due

- `due = Enabled && NextFireAt != nil && NextFireAt <= now && (RetryAt == nil || RetryAt <= now)`
  (frontera inclusiva, igual que `ExecuteTrigger`; un `RetryAt` futuro **retiene** el disparo aunque
  `NextFireAt` ya haya pasado, hasta que el backoff elapse).
- **Múltiples triggers con el mismo `NextFireAt`** entran todos (filtro `<= now`).
- **Overdues tras downtime** entran todos y se **procesan todos antes** de planificar.
- El deadline se calcula después de vaciar el conjunto de due.

---

## `Wake()` / rescheduling

### Semántica (contrato)

> **`Wake()` significa:** «el estado que determina el próximo deadline puede haber cambiado; vuelve a evaluar ahora».
>
> **`Wake()` NO significa:** «despiértate periódicamente para comprobar la DB».

`Wake()` es **únicamente un hint** para provocar un **rescan inmediato** ante un cambio de estado
persistido. No es en ningún caso un mecanismo de polling periódico. Cumple tres propiedades:

1. **No bloqueante** — el emisor nunca se bloquea ni espera al Scheduler.
2. **Coalescente** — canal `wake chan struct{}` con buffer de capacidad 1. Si el buffer ya está
   ocupado, el send se descarta: basta **un único wake pendiente**.
3. **Suficiencia** — si llegan múltiples escrituras mientras el Scheduler está procesando, un solo
   wake pendiente es suficiente: el rescan relee **todo** el estado desde SQLite, no solo un delta,
   así que una ronda captura todos los cambios acumulados.

Implementación y comportamiento:

- Canal interno `wake chan struct{}` (buffer cap 1); `Wake()` hace un send no bloqueante
  (`select { case wake <- struct{}{}: default: }`). Un slot de 1 basta porque el wake solo
  significa «reescanea», no «reescanea N veces».
- Al despertar (o tras `timer.C`), el ciclo **relee `ListEnabled` desde SQLite** y ejecuta de nuevo
  **ARMAR → DISPARAR → PLANIFICAR**. SQLite es siempre la **source of truth**; no hay estado de
  planificación en memoria que invalidar. Esto satisface «sin polling» y «sin estado persistente en
  memoria».

### Qué operaciones provocan `Wake()`

El Scheduler no recolecta `Wake()` él mismo: **los servicios de escritura** lo invocan vía la
`Rescheduler { Wake() }` inyectada opcionalmente. Mínimamente, deben despertar:

- **Alteraciones de trigger:** `Create`, `Update`, `Enable`, `Disable` (y futuro `Delete`).
- **Actualización de `Task.DueAt`** (puede desplazar `NextFireAt` derivado).
- **Cualquier otra operación que pueda cambiar el próximo deadline efectivo** de un trigger cuyo
  plazo pueda adelantarse (p. ej. un restore de `DueAt` o un cambio de `Type`/`Value`), aunque en V1
  la lista concreta son las dos anteriores.

Plazos de la llamada:

- `Wake()` debe ejecutarse **después** de que el estado persistente se haya escrito, **nunca antes**:
  el rescan solo tiene valor si ve la escritura ya cometida.

### Robustez (Wake no es crítico para la corrección)

- Un **fallo o ausencia de `Wake()` no rompe la corrección funcional**: el siguiente ciclo normal
  del Scheduler (trigger por `timer.C`, arranque o cualquier wake posterior) relee SQLite y detecta
  el estado persistido. `Wake()` solo **adelanta** la re-evaluación; el Scheduler nunca depende de él.
- **No se introduce polling periódico** para compensar un wake perdido. La cobertura de corrección
  la da el propio ciclo (deadline por `time.Timer` + relectura de SQLite), no un sondeo adicional.
- No es un bus: es una señal de hint unidireccional de bajo coste. El cableado ocurre en `main.go`,
  fase de implementación.

> Detalle de diseño adicional: como `Wake()` se descarta cuando el Scheduler está ocupado procesando,
> y el Scheduler relee todo el estado de SQLite en cada ronda, la garantía de corrección no depende
> de capturar cada wake individual — solo de que, tras cada escritura, haya **a lo sumo una** ronda
> subsiguiente que la vea. Eso se cumple por la coalescencia + relectura completa.

---

## Tratamiento de errores: terminales vs transitorios

Regla: un error **terminal** retira el trigger del conjunto activo; un error **transitorio** se
reintenta sin retirarlo. Solo errores `ctx`/DB catastróficos abortan el ciclo.

| Caso | Señal | Tipo | Conducta |
|---|---|---|---|
| Task completado/cancelado | `ErrTaskNotExecutable` | **terminal** | **Deshabilitar** el trigger (persistir `Enabled=false`) |
| Task inexistente (huérfano) | `TaskRepository.GetByID` not-found | **terminal** | **Deshabilitar** el trigger |
| Trigger malformado | `CalculateNextFireAt` error | **terminal (reversible)** | No persistir `NextFireAt` → queda `nil`, sin busy-loop; corregible |
| Acción falla | `TriggerAction.Execute` error | transitorio (reintentable) | **No consumir**; persistir `RetryAt = now + actionRetryDelay` (backoff). El trigger sigue due pero queda retenido hasta que elapse; reintenta entonces. |

**Por qué terminales no provocan busy-loop:**

- `ErrTaskNotExecutable` y task inexistente: deshabilitar los retira de `ListEnabled`.
- Malformado: al no armarse, su `NextFireAt` es `nil` → nunca es `due` → excluido del deadline de planificación.

Solo errores transitorios (ej. DB) se re-intentan; un fallo de DB transitorio no deshabilita.

---

## Comportamiento con `NextFireAt = nil`

- **FASE ARMAR** lo computa y persiste. Si no se puede armar (malformado o `DueAt` ausente),
  queda `nil`, habilitado y fuera del deadline → sin busy-loop.
- `ExecuteTrigger` con `NextFireAt=nil` devuelve `ErrTriggerNotScheduled` (terminal reversible);
  un trigger así no debería llegar a `ExecuteTrigger` porque la FASE DISPARAR requiere `!= nil`.

---

## Comportamiento ante cambios de `Task.DueAt`

- `TaskService.Update` cambia `DueAt` → invalida `NextFireAt` de los triggers derivados
  (`before_due`/`after_due`) de esa tarea vía **`TriggerRepository.ClearDerivedNextFireAt`**.
- El Scheduler relee y **rearma** esos triggers desde el nuevo `DueAt` en la FASE ARMAR.
- Los triggers `at` no se tocan (son absolutos).
- `DueAt → nil`: el trigger queda habilitado con `NextFireAt=nil`; `CalculateNextFireAt` falla
  con `ErrTaskDueAtMissing` → no se arma → excluido del deadline; se re-activa si se restaura `DueAt`.

---

## Concesiones documentadas (fuera de V1)

1. **`ClearDerivedNextFireAt` es best-effort**: `TaskService.Update` lo invoca y **ignora** el
   error (mismo principio de resiliencia que `emit`). Una falla del repository no bloquea el
   update del task.
2. **No hay transacción entre `Task` y `Trigger`**: `Update` persiste el task y luego la
   invalidación de triggers en operaciones independientes. La **consistencia transaccional
   Task↔Trigger queda fuera de V1**; la ventana de incoherencia se autodisuelve cuando el
   Scheduler relee SQLite (la fuente de verdad converge al siguiente ciclo).

    3. **Error de `ListEnabled`: no se reanuda solo**: ante un error persistente de `ListEnabled`
       (p. ej. `database is locked`) el ciclo aborta y `Run` espera solo en `Wake()` o `ctx.Done()`
       (sin busy-loop). Una **recuperación espontánea de un error transitorio de DB, sin un
       `Wake()` posterior, no reanuda el scheduler por sí sola**. Es una concesión conocida y
       deliberada de V1: no se introduce retry periódico ni timer artificial para recuperar
       errores de DB. Un `Wake()` de un servicio de escritura, o un reinicio del proceso, reanuda el ciclo.

---

## Fuera de alcance del Scheduler V1

Timers múltiples, polling, Telegram, notificaciones, event bus, recurrencia, IA, nuevas
dependencias. El núcleo V1 descrito arriba (ARMAR → DISPARAR → PLANIFICAR, `RetryAt`, `Wake()`,
at-least-once, shutdown limpio) **está implementado** en `internal/scheduler/`; lo marcado como
«futuro» en este documento permanece fuera de V1.
