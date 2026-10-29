# alter

Personal virtual secretary — modular, extensible, low-resource.

## Estado actual

La **fase 1 del canal Telegram ↔ Pi está implementada**: Telegram es la única
entrada y salida, Pi es el motor y responde con markdown rico (tablas, listas,
citas, botones). Se pueden enviar y recibir archivos, y Pi escribe en el vault
global con sus propias herramientas de terminal: puede ejecutar `bash`, editar y
escribir como en una sesión normal. Solo dos tools son propias de Alter
(`send_file`, `ask_options`), las que un shell no puede suplir.

Antes de esa fase ya existían, y siguen funcionando sin el bridge activo: SQLite
como fuente de verdad, tareas y recordatorios con comandos de Telegram, el
Scheduler y el Pi Agent opcional.

Detalle operativo (variables, arranque, qué está probado y qué no, limitaciones):
**[`docs/bridge-fase1.md`](docs/bridge-fase1.md)**.

Pendiente: fase 2 (skills para casos concretos) y fase 3 (rutinas programadas).

## Principios

- **Modular desde el primer día** — las integraciones se añaden como paquetes bajo
  `internal/`, sin tocar el núcleo.
- **SQLite primero, PostgreSQL preparado** — la capa de datos está tras interfaces,
  así que el camino de migración es claro.
- **Dependencias mínimas** — solo stdlib hasta que una necesidad real justifique un
  paquete externo.
- **Poco consumo** — pensado para máquinas pequeñas, sin atención constante.

## Estructura

```
alter/
├── cmd/alter/                  # Entry point y wiring del bridge
├── internal/
│   ├── adapter/telegram/       # Cliente Bot API, poller, canal
│   ├── bridge/                 # Sesión Pi persistente (pi --mode rpc)
│   ├── tools/                  # Servidor de tools deterministas + extensión Pi
│   ├── vault/                  # Almacén de notas y adjuntos
│   ├── config/                 # Configuración por entorno + .env
│   ├── scheduler/              # Triggers y recordatorios
│   └── storage/sqlite/         # Persistencia
├── alter-persona.md            # Persona y contrato de formato de Pi
├── docs/
└── README.md
```

## Build & Run

```bash
go build -o alter ./cmd/alter
./alter
```

O directamente:

```bash
go run ./cmd/alter
```

Requiere `TELEGRAM_TOKEN` y `TELEGRAM_CHAT_ID`; el resto tiene valores por defecto
(véase `docs/bridge-fase1.md`). Para el canal rico completo:

```bash
ALTER_BRIDGE_ENABLED=true go run ./cmd/alter
```

## Requirements

- Go 1.25+
- Git
- `pi` (CLI de Pi) en el `PATH` o en `ALTER_PI_BIN`, para el bridge
