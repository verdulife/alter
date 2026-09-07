# secretary

Personal virtual secretary — modular, extensible, low-resource.

## Purpose

A local-first tool that will progressively grow into a personal secretary with integrations (calendar, email, messaging). The first communication channel will be Telegram, but nothing is implemented yet.

## Principles

- **Modular from day one** — integrations are added as packages under `internal/`, never by modifying the core.
- **SQLite first, PostgreSQL ready** — data layer abstracted via interfaces so the migration path is clear.
- **Minimal dependencies** — only stdlib until a real need justifies an external package.
- **Low resource usage** — designed to run on small machines without constant attention.
- **No AI, no tasks, no Telegram yet** — those will come later.

## Project Structure

```
secretary/
├── cmd/secretary/      # Entry point
├── internal/
│   └── config/         # Application configuration
└── README.md
```

Future additions:

```
internal/
├── storage/            # Database abstraction (SQLite → PostgreSQL)
├── calendar/           # Calendar integration
├── email/              # Email integration
└── channel/            # Communication channels (Telegram, etc.)
```

## Build & Run

```bash
go build -o secretary ./cmd/secretary
./secretary
```

Or directly:

```bash
go run ./cmd/secretary
```

## Requirements

- Go 1.24+
- Git
