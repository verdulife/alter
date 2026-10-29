# Límites de ALTER

> Documento de producto, no de implementación. Define qué es Alter, qué puede
> hacer y qué no. Si algo de aquí cambia, se cambia aquí primero.

## Principio

**Alter es tu gentle-shell, por Telegram.** No un asistente recortado con un
canal bonito: el mismo agente que tienes en la terminal, con las mismas
herramientas, la misma potencia y la misma disciplina, pero con Alter como
identidad y el vault como memoria.

Todo lo que sigue se deriva de ese principio, no al revés.

## Identidad

Alter **no es el Gentleman**. Es un secretario personal con la disciplina de
Gentleman: planifica antes de actuar, delega cuando la tarea lo merece, verifica
lo que afirma y protege al revisor.

Cuando habla por Telegram lo hace como Alter: breve, en español, con markdown
rico, sin volcar código ni salidas de herramientas. Cuando trabaja, trabaja como
Gentleman: estructura, evidencia y verificación.

La identidad se impone por prompt, no por limitación de capacidades. Quitarle
herramientas para que "se comporte" es lo que hacía la versión anterior y era un
error: un agente sin `bash` no es un agente contenido, es un agente inútil.

## Capacidades

Tiene exactamente las herramientas de una sesión de terminal: `bash`, `edit`,
`write`, `read`, `grep`, `find`, `ls`, más las que aporten los paquetes cargados
(memoria, web, subagentes).

Solo dos tools son propias de Alter, y lo son porque una terminal no puede
hacerlo: `send_file`, para mandarte un archivo del vault, y `ask_options`, para
preguntarte con botones. El vault no tiene vía propia de escritura: Pi lo escribe
con `write` y `edit`, como cualquier otro directorio. Una segunda forma de
escribir los mismos archivos no añadía capacidad, solo un contrato más que
mantener y una ruta más que contener.

Carga la configuración global de Pi: gentle-pi, gentle-engram, pi-btw,
pi-web-access y sus skills. No hay "carga limpia".

Directorio de trabajo: el repositorio de Alter. Ve todo lo que ves tú: `bash`
llega a cualquier ruta absoluta y el vault sigue en `$HOME/vault`.

Sus skills son del proyecto, no globales: viven en `.agents/skills/` del
repositorio y Pi las descubre desde su directorio de trabajo hacia arriba. No
aparecen en ninguna sesión ajena a Alter, y de ellas solo entra en contexto el
nombre y la descripción hasta que el modelo las invoca.

## Aprobaciones

Pedir permiso para cada `bash`, `write` y `edit` era un peaje, no una defensa: un
muro de botones se aprueba sin leer. **El permiso es para lo que no se puede
deshacer**, y la extensión decide caso por caso en `tool_call`:

| Herramienta | Pide confirmación |
|---|---|
| `edit` | nunca: es un cambio quirúrgico y el diff es la revisión |
| `write` | cuando el archivo ya existe, o cuando la ruta cae fuera de `$HOME` |
| `bash` | cuando el comando puede destruir, publicar o escalar |
| `read`, `grep`, `find`, `ls`, `send_file`, `ask_options` | nunca |
| cualquier otra (paquetes, MCP) | siempre: sin regla no hay decisión |

La ruta se resuelve como la resuelve Pi: `~` y `~/x` contra `$HOME`, el resto
contra el directorio de trabajo de la sesión. `~usuario` no se expande (Pi
tampoco lo hace), así que se pregunta.

Qué pide `bash`, sobre el comando entero y no solo sobre su primera palabra
(`sudo rm` y `FOO=1 rm` son el mismo acto que `rm`, y `git -C x push` publica
igual que `git push`):

- borrar o truncar: `rm`, `rmdir`, `unlink`, `shred`, `truncate`, `dd`, `mkfs`,
  `find -delete`/`-exec`;
- sobrescribir por redirección: `>` sobre un archivo que ya existe, y `>` o `>>`
  sobre una ruta sensible (`~/.ssh`, `~/.bashrc`, los arranques de shell, `/etc`,
  `/usr`…). `>>` añade y no destruye, así que solo pregunta por esas rutas; una
  ruta con variables (`$HOME/…`) no se puede resolver y también pregunta;
- `mv` cuyo destino ya existe;
- permisos y propiedad: `chmod`, `chown`, `chgrp`, `chattr`;
- procesos y servicios: `kill`, `pkill`, `killall`, `systemctl`, `service`,
  `shutdown`, `reboot`, `poweroff`;
- la puerta de atrás de los privilegios: `sudo`, `doas`;
- estado del sistema y paquetes: `apt`, `apt-get`, `dpkg`, `brew`, `snap`,
  `npm i -g`, `pip install`, `cargo install`, `crontab`;
- git que publica o reescribe: `push`, `reset --hard`, `clean -f`, `rebase`,
  `checkout --`, `restore`, `commit --amend`, `branch -d`, `tag -d`,
  `stash drop`, `filter-branch`;
- guiones remotos: `curl` o `wget` canalizados a un intérprete;
- contenedores: `docker rm`, `rmi`, `prune`, `compose down -v`.

Lo que el clasificador no puede decidir, pregunta: comillas sin cerrar, una ruta
que no resuelve, una línea partida en demasiadas piezas. El cuerpo de un heredoc
se clasifica línea a línea, así que una línea que empiece por un programa
peligroso pregunta aunque sea texto: se equivoca pidiendo permiso, nunca al
revés.

El botón recorre el mismo camino: la pregunta viaja a Telegram como dos botones
y tu pulsación la responde; un rechazo, una cancelación o un error bloquean la
llamada, y sin respuesta en el plazo **se bloquea**. Cada decisión deja una línea
en el diario (`alter-gate: allow bash …`, `alter-gate: ask (git push publica en
el remoto) bash …`), así que un turno real se puede auditar después.

Sin terminal no hay nadie mirando: el botón es la única aprobación real que
existe, y por eso el diario es la otra mitad de la defensa.

## Límites duros

| Límite | Valor | Motivo |
|---|---|---|
| Usuarios | Solo tu chat ID | Un dueño, un flujo |
| Turnos | Uno a la vez | La sesión es un hilo único |
| Preguntas pendientes | Una | Evita respuestas cruzadas |
| Timeout de turno | Configurable, 300 s por defecto | Un `bash` largo necesita margen |
| Archivos entrantes | 20 MB con la API cloud; sin límite con Bot API server propio | Techo de descarga de Telegram |
| Directorio de trabajo | El repositorio de Alter | Es tu gentle-shell, y las skills del proyecto se descubren desde ahí |

## Riesgos aceptados

Estos no son defectos: son el precio de lo que pediste. Se aceptan sabiéndolos.

1. **Control total del servidor desde el móvil.** Con `bash` y `write` en `$HOME`,
   un mensaje puede borrar, cifrar o exfiltrar cualquier cosa tuya. La única
   frontera es el token del bot y tu chat ID.
   - Si pierdes el móvil: el chat sigue autorizado hasta que revoques el token.
   - Si el token se filtra: cualquiera con él y tu chat ID puede operar. Un bot
     solo atiende a quien le escribe primero, pero el token da acceso a la API.
2. **`$HOME` incluye secretos**: `~/.ssh`, `~/.config`, `.env`, credenciales de
   git y de proveedores. No hay sandbox.
3. **Aprobaciones con latencia.** Cada acción sensible espera un botón. Si no
   estás mirando el móvil, la tarea se bloquea a los N segundos.
4. **Sin aislamiento entre conversación y sistema.** Una inyección de prompt en
   un contenido que Pi lea (`fetch_content`, un PDF, un archivo del usuario)
   podría intentar dirigir sus herramientas. El gate de confirmación es la
   defensa principal, no la única: conviene revisar qué se aprueba, y ahora la
   mayoría de las acciones no se aprueba: se revisa después, en el diario.
5. **TOCTOU en el vault**: la contención de rutas de `send_file` se comprueba y
   luego se lee. Con `bash` disponible, esa defensa deja de ser una frontera real.
6. **El gate es un clasificador, no un sandbox.** Reconoce programas,
   subcomandos y redirecciones; una forma que no conozca —un script propio, un
   binario que borra por su cuenta, un alias dentro de un archivo— pasa sin
   botón. Y como `bash` corre sin permiso dentro de `$HOME`, un turno puede
   borrar o cifrar archivos tuyos sin que veas nada. La red de seguridad ya no es
   el botón: es el diario y lo que revises en él.
7. **`write` de un archivo nuevo dentro de `$HOME` no pregunta**, tampoco en
   rutas como `~/.ssh/authorized_keys` o `~/.bashrc` cuando aún no existen. Crear
   un archivo no destruye nada y pedir permiso por cada archivo nuevo volvía a
   ser el peaje; lo que queda como frontera es que ese archivo no exista todavía.
   El `bash` equivalente (`echo … >> ~/.ssh/authorized_keys`) sí pregunta, porque
   la redirección escribe sobre una ruta sensible.

## Lo que Alter no hace

- No atiende a nadie que no seas tú.
- No mantiene varias conversaciones en paralelo ni reparte turnos.
- No conserva los comandos de barra del bot antiguo (`/nueva`, `/recordar`,
  `/listar`…): en modo bridge, todo texto va a Pi.
- No expone nada a Internet: Telegram es la única entrada, con conexión saliente.
- No ejecuta trabajo programado. Eso es la fase 3, con cron o daemon despertando
  a Pi.
- No tiene skills globales. Las suyas son del proyecto (`.agents/skills/`) y solo
existen en las sesiones que arrancan desde el repositorio. La fase 2 añadirá más;
la primera es el contrato del vault.

## Qué cambia respecto a lo construido

Lo entregado en la fase 1 se hizo con la restricción heredada de solo lectura.
Quedan tres cambios, en este orden:

1. **T11 — Permisos y ámbito**: allowlist completa, carga global de Pi, timeout
   configurable. Se retira `--no-skills`, `--no-extensions`, `--no-context-files`
   y la allowlist de solo lectura.
2. **T12 — Identidad**: reescribir `alter-persona.md` con la disciplina de
   Gentleman y una identidad Alter explícita; imponerla por `tool_call`/
   `before_agent_start` si el orden de prompts no basta. Verificar empíricamente
   qué prompt ve el modelo.
3. **T13 — Aprobaciones**: extensión que intercepta `tool_call`, pide
   confirmación y la entrega al bridge; el bridge convierte cada
   `extension_ui_request` de tipo `confirm`/`select` en botones y responde con la
   pulsación o con `cancelled`.

Y una corrección de alcance, **T17**: con `bash`, `write` y `edit` disponibles,
`save_note` y `append_note` eran una segunda forma de escribir los mismos
archivos. Se retiran. Quedan dos tools deterministas, `send_file` y
`ask_options`, porque son lo único que Pi no puede hacer por sí mismo. El vault
sigue siendo el almacén: lo escribe Pi, y la contención de rutas sigue viva donde
importa, en la única tool que lee del vault por nombre.

Y uno que no es diseño, sino despliegue: **T14 — Sustituir el servicio**. El
`alter.service` actual ejecuta un binario del 16 de octubre. Mientras siga vivo,
el bridge no puede recibir mensajes (mismo token, `409 Conflict`) y todo lo que
responda será del bot antiguo.
