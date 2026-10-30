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
muro de botones se aprueba sin leer. El dueño eligió qué merece un botón, y son
tres cosas y solo tres: **perder datos, publicar de forma irreversible y cambiar
las herramientas de Pi**, con dos excepciones puntuales que también lo merecen y
que no se amplían: **escribir dentro de un directorio `.ssh`** y **escalar a los
permisos de otro usuario con `sudo`/`doas`**. Todo lo demás pasa sin pregunta y
queda en el diario. La extensión decide caso por caso en `tool_call`:

| Herramienta | Pide confirmación |
|---|---|
| `bash` | solo por las familias de la lista de abajo, y siempre por `sudo`/`doas` |
| `write` | solo si la ruta cae dentro de un directorio `.ssh`; en cualquier otra, nunca |
| `edit` | nunca: es un cambio quirúrgico y el diff es la revisión |
| `read`, `grep`, `find`, `ls`, `send_file`, `ask_options` | nunca |
| cualquier otra (paquetes, MCP) | solo si su nombre dice que destruye: `delete_file`, `removeRepo`, `reset_session`, `truncate_table` |

Una herramienta sin regla propia **no pregunta por defecto**. Pi carga la
configuración global, así que el modelo lleva encima `web_search`,
`fetch_content`, la memoria y los subagentes: un botón por cada una era el mismo
peaje, una capa más afuera. La señal que queda es el nombre, y basta para lo que
importa: si contiene `delete`, `remove`, `drop`, `destroy`, `purge`, `wipe`,
`truncate`, `reset`, `kill`, `unlink` o `rm` como palabra o como pieza del nombre
(`delete_file`, `removeRepo`, `reset_session`), pregunta, y de ahí no se escapa:
el nombre no cambia y la regla no falla abierta. La comparación es por piezas y
no por subcadenas, así que `address_lookup` no casa con `rm`. Todo lo demás se
permite y queda en el diario, que es donde se revisa.

Un nombre que destruye sin decirlo (`clear`, `erase`, `forget`, `discard`) pasa
sin botón: la lista es cerrada y se amplía aquí cuando aparezca uno.

La ruta se resuelve como la resuelve Pi: `~` y `~/x` contra `$HOME`, el resto
contra el directorio de trabajo de la sesión. `~usuario` no se expande (Pi
tampoco lo hace), así que un `mv` hacia esa ruta pregunta.

Qué pide `bash`, sobre el comando entero y no solo sobre su primera palabra
(`sudo rm` y `FOO=1 rm` son el mismo acto que `rm`, y `git -C x push` publica
igual que `git push`). Son tres familias y solo tres, más la escalada de
privilegios:

- **pérdida de datos**: `rm`, `rmdir`, `unlink`, `shred`, `truncate`, `dd`,
  `mkfs`, `fdisk`, `parted` y `wipefs` —un disco formateado o repartido no se
  recupera—, `find -delete` y las formas de `find` que truncan un archivo propio,
  `mv` cuyo destino ya existe, y `find -exec`/`-ok` cuando el programa ejecutado
  pierde datos (`find … -exec rm`, `-exec sh -c 'rm x'`). `find -exec` se clasifica
  por el programa que ejecuta, así que `find … -exec ls` y `find … -exec chmod` no
  preguntan. `docker`/`podman`: `rm`, `rmi`, `prune`, `volume rm` y `compose down
  -v`, que borran contenedores, imágenes o volúmenes;
- **git que pierde datos o reescribe historia publicada**: `push`, `reset --hard`,
  `clean -f`, `rebase`, `checkout --`, `restore`, `commit --amend`, `branch
  -d`/`-D`, `tag -d`, `stash drop`/`clear` y `filter-branch`;
- **publicación irreversible**: `npm`, `yarn`, `pnpm` y `bun` `publish` —también
  detrás de `npx`/`npm exec`, que preguntan por la publicación y no por el paquete
  descargado («Va a publicar un paquete de forma irreversible.»)—, `cargo publish`,
  `gem push`, `twine upload`, `helm push`, `docker` y `podman` `push` («Va a
  publicar una imagen de forma irreversible.»), `gh release create`/`delete` («Va
  a publicar o borrar una versión de forma irreversible.»), y `pi install` (salvo
  `--local`/`-l`) y `pi remove`, que cambian las herramientas, skills y prompts en
  los que confías porque escriben `~/.pi/agent/settings.json` («Va a cambiar las
  herramientas de Pi en las que confías.»). Consultar no pregunta: `npm
  view`/`info`/`outdated`, `cargo search`, `helm list`, `gh release list`/`view`,
  `docker images`, `pi list`;
- **escalada de privilegios**: `sudo` y `doas` preguntan **por sí mismos**, con
  cualquier comando detrás —`sudo ls`, `sudo -i`, `sudo bash -c '…'`—, porque
  alcanzar los permisos de otro usuario no se deshace y `sudo bash -c '…'` llega a
  un shell de root. La frase es «Va a escalar a los permisos de otro usuario.». El
  comando de dentro se sigue clasificando y la pregunta conserva lo más concreto:
  `sudo rm x` pregunta por el borrado («Va a borrar archivos.»), no por la
  escalada.

De `write` pregunta un solo caso: que la ruta caiga dentro de un directorio `.ssh`
—`~/.ssh/…`, `$HOME/.ssh/…`, una absoluta con `/.ssh/` o una relativa que resuelva
ahí—, porque ahí viven las claves y el `authorized_keys` que deciden quién entra a
tus servidores, y sobrescribirlo entrega acceso en vez de perder datos. La frase es
«Va a escribir en tu carpeta .ssh, donde las claves dan acceso a tus servidores.».
Todo lo demás sigue mudo, incluido lo demás sensible: `.gnupg`, `.aws`, `/etc`,
`.bashrc`, un archivo que ya existe y una ruta fuera de `$HOME`.

Lo que **dejó de preguntar**, por decisión explícita del dueño, y ya no tiene botón
ni forma de recuperarse:

- permisos y atributos: `chmod`, `chown`, `chgrp`, `chattr`;
- procesos y servicios: `kill`, `pkill`, `killall`, `service`, `systemctl`,
  `shutdown`, `reboot`, `poweroff`;
- instaladores del sistema: `apt`, `apt-get`, `dpkg`, `brew`, `snap`, `npm i -g`,
  `pip install`, `cargo install`, `crontab`;
- intérpretes y lanzadores de paquetes: `python3 -m pip install`, cualquier otro
  `python3 -m <módulo>`, `python3 -c`, `node -e`, `perl -e`, `ruby -e`, `npx`,
  `npm exec`, `pnpm dlx`, `yarn dlx`, `bunx`;
- `curl`/`wget` canalizados a un shell o a un intérprete;
- redirecciones `>` y `>>`: nunca preguntan, tampoco sobre un archivo que ya
  existe ni sobre una ruta sensible (`~/.ssh`, `~/.bashrc`, los arranques de
  shell, `/etc`, `/usr`…). Una ruta con variables (`$HOME/…`) tampoco pregunta.
  Esto es `bash`: la guarda del `.ssh` de arriba la pone la herramienta `write`, no
  una redirección, así que `echo … > ~/.ssh/authorized_keys` sigue en silencio.

Ejecutar un script que ya existe **no pregunta**: `python3 script.py`, `node
script.js`, `bash instalar.sh` y `./binario` son la misma clase que `go test`.
Esa fue una decisión anterior y sigue en pie: lo que hay dentro del archivo no se
lee, y su consecuencia está en el riesgo 6.

Lo que el clasificador no puede decidir, pregunta: comillas sin cerrar, una ruta
que no resuelve, una línea partida en demasiadas piezas. El cuerpo de un heredoc
se clasifica línea a línea, así que una línea que empiece por un programa
peligroso pregunta aunque sea texto: se equivoca pidiendo permiso, nunca al
revés.

La pregunta está escrita para leerla en un móvil: el título es siempre «¿Lo
permito?» y debajo va una frase con la consecuencia, en segunda persona, que no
nombra el programa salvo que el programa sea el asunto («Va a borrar archivos.»,
«Va a sobrescribir un archivo.» para el `mv`, «Va a cambiar las herramientas de Pi
en las que confías.»). El comando viaja dentro de un bloque de código, con sus
saltos de línea, para poder juzgarlo línea a línea; uno de más de 800 caracteres se
recorta por el medio, marcando el hueco con `…` y conservando el principio y el
final, que es donde suele estar lo peligroso.

El botón recorre el mismo camino: la pregunta viaja a Telegram como dos botones
—cada uno en su fila, a todo el ancho, y el de aprobar con estilo `success`— y tu
pulsación la responde; un rechazo, una cancelación o un error bloquean la
llamada, y sin respuesta en el plazo **se bloquea**. Cada decisión deja una línea
en el diario (`alter-gate: allow bash …`, `alter-gate: ask (git push publica en
el remoto) bash …`), así que un turno real se puede auditar después. Esa línea
sigue siendo la técnica y la completa —herramienta, regla aplicada y vista previa
del argumento—, y por eso distingue `git push` de `git reset --hard` aunque la
pregunta use la misma frase para los dos.

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

Lo que esta reducción deja sin ninguna protección, dicho sin adornos: un agente
confundido o comprometido —por una inyección de prompt, por un contenido que lea o
por un error suyo— puede **reescribir o sobrescribir cualquier archivo** —el único
botón que queda ahí es el de `~/.ssh`—, **cambiar permisos y dueños**, **parar,
reiniciar o alterar servicios y procesos**, **instalar software**, **programar
tareas** y **ejecutar código descargado**, sin que aparezca un solo botón. El gate
ya no cubre nada de eso: solo sigue parando lo que borra datos, lo que publica de
forma irreversible, lo que cambia las herramientas de Pi, la escritura en `~/.ssh`
y la escalada con `sudo`/`doas`.

1. **Control total del servidor desde el móvil.** Con `bash` y `write` en `$HOME`,
   un mensaje puede borrar, cifrar o exfiltrar cualquier cosa tuya. La única
   frontera es el token del bot y tu chat ID.
   - Si pierdes el móvil: el chat sigue autorizado hasta que revoques el token.
   - Si el token se filtra: cualquiera con él y tu chat ID puede operar. Un bot
     solo atiende a quien le escribe primero, pero el token da acceso a la API.
2. **`$HOME` incluye secretos**: `~/.ssh`, `~/.config`, `.env`, credenciales de
   git y de proveedores. No hay sandbox; el único de esos sitios con botón es
   `.ssh`.
3. **Aprobaciones con latencia.** Cuando algo pregunta, espera un botón, y sin
   respuesta la tarea se bloquea a los N segundos. Ahora pregunta muy poco, y por
   eso el silencio ya no informa: que no aparezca un botón no significa que la
   acción sea inocua.
4. **Sin aislamiento entre conversación y sistema.** Una inyección de prompt en
   un contenido que Pi lea (`fetch_content`, un PDF, un archivo del usuario)
   podría intentar dirigir sus herramientas, y desde esta reducción la mayor parte
   de lo que intentaría —escribir un archivo, tocar un arranque de shell, cambiar
   permisos, instalar un paquete, ejecutar lo descargado— ya no tiene botón. El
   gate dejó de ser la defensa principal: lo que queda es el diario y lo que
   revises en él.
5. **TOCTOU en el vault**: la contención de rutas de `send_file` se comprueba y
   luego se lee. Con `bash` disponible, esa defensa deja de ser una frontera real.
6. **El gate es un clasificador, no un sandbox.** Reconoce los programas de tres
   familias y la escalada; todo lo que no reconozca pasa sin botón, y esa lista es
   hoy la mayor parte de un turno: `chmod`, `systemctl`, `kill`, `apt`, `pip`,
   `npx`, `curl … | sh` y cualquier redirección. Un script propio, un binario que
   borra por su cuenta, un alias dentro de un archivo o un instalador metido en un
   `python3 -` pasan igual, y también un `rm` escrito de una forma que el lector no
   entienda. La red de seguridad ya no es el botón: es el diario y lo que revises
   en él.
7. **`write` no pregunta salvo dentro de `.ssh`**, tampoco cuando el archivo ya
   existe, ni fuera de `$HOME`, ni sobre `~/.bashrc`, `~/.gnupg`, `~/.aws`, `/etc`
   o una unidad de systemd. Un turno puede reemplazar el contenido entero de
   cualquiera de esos archivos y no verás un botón: lo verás en el diario, si lo
   miras. El `bash` equivalente escribe igual de silencioso, `echo … > ~/.bashrc`
   incluido. La única frontera que queda es el token del bot y tu chat ID.

## Lo que Alter no hace

- No atiende a nadie que no seas tú.
- No mantiene varias conversaciones en paralelo ni reparte turnos.
- No conserva los comandos de barra del bot anterior (salvo `/reset`): en modo
  bridge, todo lo demás va a Pi.
- No expone nada a Internet: Telegram es la única entrada, con conexión saliente.
- No ejecuta trabajo programado. Eso es la fase 3, con cron o daemon despertando
  a Pi.
- No tiene skills globales. Las suyas son del proyecto (`.agents/skills/`) y solo
existen en las sesiones que arrancan desde el repositorio. La fase 2 añadirá más;
la primera es el contrato del vault.

## Qué cambió respecto a lo construido

Lo entregado en la fase 1 se hizo con la restricción heredada de solo lectura.
Estos son los cambios que la retiraron, en este orden:

1. **T11 — Permisos y ámbito**: allowlist completa, carga global de Pi, timeout
   configurable. Se retiró `--no-skills`, `--no-extensions`, `--no-context-files`
   y la allowlist de solo lectura.
2. **T12 — Identidad**: reescribir `alter-persona.md` con la disciplina de
   Gentleman y una identidad Alter explícita; imponerla por `tool_call`/
   `before_agent_start` si el orden de prompts no basta. Verificar empíricamente
   qué prompt ve el modelo.
3. **T13 — Aprobaciones**: extensión que intercepta `tool_call`, pide
   confirmación y la entrega al bridge; el bridge convierte cada
   `extension_ui_request` de tipo `confirm`/`select` en botones y responde con la
   pulsación o con `cancelled`.

Y una corrección de alcance, **T17**: con `bash`, `write` y `edit` disponibles, las
dos tools de escritura de notas eran una segunda forma de escribir los mismos
archivos. Se retiraron. Quedan dos tools deterministas, `send_file` y
`ask_options`, porque son lo único que Pi no puede hacer por sí mismo. El vault
sigue siendo el almacén: lo escribe Pi, y la contención de rutas sigue viva donde
importa, en la única tool que lee del vault por nombre.

Y uno que no es diseño, sino despliegue: **T14 — Sustituir el servicio**, hecho.
`alter.service` ejecuta el binario del bridge (`bin/alter`) con el repositorio como
`WorkingDirectory`, así que no queda un bot antiguo ocupando el mismo token ni
provocando `409 Conflict`.
