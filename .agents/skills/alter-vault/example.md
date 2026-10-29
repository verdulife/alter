Ejemplo de referencia del contrato de `ALTER_VAULT` (ver `SKILL.md`). No es una
entrada del vault: son dos ficheros de ejemplo con la forma exacta que debe tener
cada tipo. Cópialos cambiando los valores, no la estructura.

## Tarea → `$HOME/vault/tasks/2026-10-29-renovar-dni.md`

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

## Recordatorio → `$HOME/vault/reminders/2026-10-29-llamar-a-mama.md`

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

## Comprobación

Con cualquiera de los dos ficheros en el vault, un shell lo encuentra sin parser
YAML:

```bash
grep -l '^status: open$' "$HOME/vault"/*/*.md 2>/dev/null
grep -l '^remind_at: 2026-10-30' "$HOME/vault/reminders"/*.md 2>/dev/null
```
