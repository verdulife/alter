---
name: alter-links
description: Procedimiento para un enlace que llega por el canal. Úsala cuando el mensaje traiga una URL suelta, «guárdame esto», un vídeo, un artículo o un enlace que no sea una pregunta. Decide si se guarda, qué se escribe y dónde queda. El formato exacto de la entrada lo manda alter-vault.
---

# Enlaces

Carpetas, claves, valores admitidos y nombre de fichero: `alter-vault`. Aquí
solo está qué hacer cuando llega una URL.

## Guardar o contestar

- **Guardar sin preguntar**: el mensaje es la URL y nada más, o la URL viene con
  «guárdame esto», «para luego», «míralo cuando puedas». No hay pregunta detrás.
- **Contestar, no guardar**: la URL viene dentro de una pregunta («¿esto es lo
  que decías?», «resúmelo», «¿qué te parece?»). Se responde y no se escribe nada
  en el vault, salvo que el Dueño lo pida en el mismo mensaje.
- **Duda**: contesta primero. Guardar es un paso extra que se puede pedir luego.

## Título, canal y fecha

1. `fetch_content` sobre la URL es lo barato: trae título y canal o sitio.
2. Si falla, no lo reintentes ni bloquees la respuesta: guarda lo que ya sabes y
   dilo en una línea. Título, el host o el texto que acompañaba a la URL; canal,
   `desconocido`.
3. Lo que no se deduce no se inventa: ni el canal, ni la fecha, ni el autor.

## Dónde queda

- Una entrada por enlace, en `$HOME/vault/notes/`, `type: note`, con `enlace` en
  `tags` delante de lo que aporte el tema: `tags: "enlace,video,ia"`.
- El resto de claves y el nombre del fichero, como dice `alter-vault`.
- Cuerpo: `# <título>` y, debajo, la línea del enlace con la convención que ya
  usa el vault:

```markdown
- [git no aguanto 🤯](https://youtube.com/shorts/RvyO6I6u1n8) — canal Machini, 15 oct 2026
```

Título del recurso entre corchetes, nunca la URL. Después del guion, canal o
sitio y la fecha del recurso; si no hay fecha, la de hoy. Si el Dueño dijo por
qué lo mandaba, una línea de contexto debajo.

## Lo que no se guarda

- **Secretos**: URLs con token, `?key=`, firma o enlace de sesión. Si hay
  pregunta, se contesta; no se escriben.
- **Seguimiento**: quita `utm_*`, `fbclid`, `gclid` y similares antes de
  escribir. La URL limpia sirve igual y no ensucia la nota.
- **Duplicados**: busca antes con `grep -ril '<url>' "$HOME/vault" 2>/dev/null`.
  Si ya está, no crees otra entrada: di en una línea que ya la tenías y de cuándo.

Al terminar, una línea: qué enlace, en qué nota y si faltó metadata.
