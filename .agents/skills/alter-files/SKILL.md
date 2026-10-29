---
name: alter-files
description: Procedimiento para un archivo que llega a inbox/. Úsala cuando el mensaje traiga un archivo, documento, foto o audio y haya que decidir si se lee, se resume o solo se acusa recibo, si se queda en inbox/ y cuándo pasa a notes/ junto a una nota. El formato de la nota lo manda alter-vault.
---

# Archivos

El prompt ya trae la ruta del archivo en el vault (`inbox/<fecha>-<nombre>`), el
tipo MIME y, si lo hubo, el caption. No busques el archivo ni adivines el
nombre: usa esa ruta tal cual y, si no es absoluta, antepón `$HOME/vault/`. El
formato de la entrada lo manda `alter-vault`.

## Antes de nada: comprobar qué es

El nombre no es prueba del contenido. Un `factura.pdf` puede ser la foto de un
ticket y un `.docx` puede ser un `.zip`. Míralo antes de actuar:

```bash
file "$HOME/vault/inbox/<archivo>"
head -c 400 "$HOME/vault/inbox/<archivo>"   # si resulta ser texto
```

Si el contenido no cuadra con el nombre, dilo en una línea y no sigas como si
cuadrara.

## Decidir con MIME y caption

El caption manda. Si pide algo concreto («resume esto», «saca las fechas»), eso
es lo que se hace.

- **Texto, markdown, PDF, CSV, código**: se lee y se resume. Sin caption, un
  resumen de tres o cuatro líneas y una pregunta corta.
- **Imagen, audio, vídeo**: no se leen. Acuse de recibo en una línea, qué es y
  una pregunta corta.
- **Sin caption y tipo opaco** (`application/octet-stream`, `.zip`): no se abre a
  ciegas. Acuse de recibo y una pregunta corta.

## Dónde se queda

- **Se queda en `inbox/`** lo de paso: una captura, un audio, algo que el Dueño
  ya ha visto o solo quería enseñar. `inbox/` es el sitio correcto, no un limbo
  del que haya que sacarlo todo.
- **Se mueve a `notes/`** lo que merece conservarse, y se queda al lado de una
  nota que lo referencia: crea la entrada con `alter-vault` (`type: note`, la
  etiqueta del tema), mueve el archivo con `mv` dentro del vault y enlázalo en el
  cuerpo con ruta relativa a la nota (`[factura-octubre.pdf](factura-octubre.pdf)`).
  Renombra solo si el nombre guardado es ilegible; si no, se conserva.

## Lo que nunca pasa

- **No se borra** un archivo del Dueño. Si sobra, se dice y se deja donde está.
- **No se copia fuera del vault**: ni a `/tmp`, ni al repositorio, ni a `$HOME`.
  Leerlo sí; sacarlo, no.
- **No se renombra** lo que se queda en `inbox/`.

Al terminar, una línea: qué era, qué has hecho con él y dónde está.
