# Fase 3 — Recordatorios que suenan

> Plan de diseño aprobado. El vault es la única fuente de verdad; systemd es el
> despertador; el agente es quien redacta y actúa.

## Arquitectura

```
vault/reminders/2026-11-03-llamar-a-mama.md     ← fuente de verdad
        │
        │  alter remind sync                     ← reconcilia vault ↔ timers
        │     (al arrancar el servicio, y cuando el agente crea o edita uno)
        ▼
systemd --user: alter-remind-llamar-a-mama.timer
        │  OnCalendar=<remind_at>  Persistent=true
        ▼
alter remind fire llamar-a-mama
        ├── valida el .md con el mismo validador de `alter vault check`
        ├── si ya venció y no está disparado, sigue; si no, sale sin ruido
        ├── marca last_fired (atómico) o adelanta remind_at si es recurrente
        ├── despierta a Pi: `pi -p` con el contenido del recordatorio
        │      └── Pi redacta y avisa con `alter notify "..."`
        └── si Pi falla, `alter notify` con el título (el aviso no se pierde)
```

## Por qué así

- **Sobrevive a reinicios** porque el estado está en el vault y `sync` recrea los
  timers al arrancar.
- **Precisión al segundo**: no se comprueba cada minuto.
- **El aviso llega aunque el bridge esté caído**: el timer ejecuta un binario
  independiente, no depende del poller.
- **Puesta al día**: `Persistent=true` para systemd y, además, `sync` dispara lo
  que venció mientras la máquina estaba apagada.
- **El agente actualiza y notifica**, no un texto fijo.
- **Una sola fuente de verdad**: el `.md`. Los timers son derivados y
  reconstruibles; si desaparecen, `sync` los vuelve a crear.

## Timers transient, a propósito

`sync` usa `systemd-run --user --on-calendar=… --unit=alter-remind-<slug>`, no
ficheros de unidad. Evita escribir dos ficheros por recordatorio, `daemon-reload`
y `enable`. El precio: un reinicio de la máquina los borra, y eso lo cubre `sync`
al arrancar junto con la puesta al día.

Si en el futuro los recordatorios crecen hasta decenas, se migra a unidades
persistentes sin cambiar el contrato.

## Componentes

| Pieza | Responsabilidad |
|---|---|
| `internal/vault/reminders.go` | leer vencidos, validar, marcar `last_fired`, adelantar recurrencia. Nada de sistema. |
| `internal/reminder/scheduler.go` | hablar con `systemd-run`/`systemctl`: crear, borrar, listar. Aislado tras una interfaz para poder probarlo con un doble. |
| `cmd/alter/remind.go` | subcomandos `sync` y `fire`, y `notify` como transporte de texto. |
| `cmd/alter/bridge_wiring.go` | una llamada a `sync` al arrancar. Nada más. |
| `.agents/skills/alter-tasks` | una línea: tras crear o editar un recordatorio, ejecutar `alter remind sync`. |

## Subcomandos

```
alter remind sync            reconcilia: crea los que faltan, borra huérfanos,
                             dispara los vencidos sin disparar
alter remind sync --dry-run  dice qué haría, sin tocar systemd
alter remind fire <slug>     dispara uno (lo llama el timer; idempotente)
alter notify "<texto>"       envía un mensaje al chat del dueño
```

## Idempotencia y errores

- `last_fired` se escribe **antes** de lanzar a Pi. Un fallo de envío pierde ese
  aviso (se registra) en vez de repetirlo cada minuto.
- `fire` sobre uno ya disparado no hace nada.
- Un `.md` inválido no se dispara: se reporta y se sigue con los demás.
- `sync` nunca borra un `.md`: solo timers.

## Recurrencia

`recurrence: daily|weekly|monthly|yearly` no marca `last_fired`: adelanta
`remind_at` a la siguiente ocurrencia y `sync` reprograma. Un recordatorio
recurrente nunca "termina".

## Criterio de aceptación

1. Crear un recordatorio a dos minutos vista.
2. **Reiniciar el servicio** antes de que venza.
3. Recibir el aviso en Telegram, redactado por Pi.
4. Reiniciar otra vez: **no** se repite.
5. Crear uno con `remind_at` de hace una hora y ejecutar `sync`: se dispara.
6. Crear uno recurrente semanal: tras dispararse, su `remind_at` avanza siete
   días y el timer se reprograma.
7. Borrar un `.md` y ejecutar `sync`: su timer desaparece de
   `systemctl --user list-timers`.

## No objetivos

- Gestión por comandos de chat (`/recordar`): se sigue hablando con el agente.
- Notificaciones fuera de Telegram.
- Reintentos con backoff.
- Unidades de sistema persistentes (se migra si hace falta).

## Estado: verificado en producción (2026-10-30)

Los seis criterios se comprobaron contra el sistema real, no con dobles:

| Criterio | Evidencia |
|---|---|
| Aviso a su hora | recordatorio de prueba a las 14:45:00; el dueño lo recibió en el móvil |
| No se repite | `last_fired` escrito, timer eliminado, `list-timers` sin unidades `alter-remind-*` |
| Puesta al día | `llamar-a-mama`, vencido antes de arrancar, disparado por el catch-up y recibido |
| Recurrente | avanzó a 2026-11-06 sin escribir `last_fired`, con el timer reprogramado |
| Huérfano | fichero borrado, `sync` eliminó su timer |
| Entorno del disparo | la unit lleva `PATH` y `EnvironmentFile`; sin ellos el aviso se perdía |

Un fallo real se encontró justo aquí: las units de `systemd-run` no heredan el
entorno del servicio, así que el primer disparo marcó el recordatorio pero no
pudo avisar (`pi` no estaba en el `PATH`, y faltaba `TELEGRAM_TOKEN`). La lógica
era correcta; lo que faltaba era el entorno. Se corrigió en dos capas: la unit
declara `--setenv=PATH` y `--property=EnvironmentFile`, y `fire`/`notify` cargan
el `.env` por su cuenta como segunda red.

Deuda conocida: esa segunda red lee `<WorkDir>/.env`, pero una unit transient
corre con `$HOME` como directorio de trabajo, así que solo actúa la primera capa.
Hacerlo determinista pide `ALTER_ENV_FILE` o `WorkingDirectory=` en la unit.
