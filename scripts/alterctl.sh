#!/usr/bin/env bash
# alterctl.sh — Operaciones operativas SEGURAS de ALTER.
#
# ALTER = systemd user unit "alter.service"
# Config = /home/balneario/alter/.env (EXCLUSIVA de ALTER)
# Bot    = AlterYouBot (verificado vía Telegram getMe, el token jamás se imprime)
#
# Reglas de aislamiento (obligatorias a partir de la auditoría de septiembre 2026):
#   - ALTER se gestiona SOLO con: systemctl --user ... alter.service
#   - NUNCA pgrep/pkill/killall ni matar procesos por nombre (Telegram).
#   - NUNCA tocar chistes-bot.service ni el bot BalnearioServerBot.
#   - NUNCA usar ~/scripts/tg.sh ni machine-status-notify.sh (pertenecen a chistes).
#   - Si cualquier comprobación de identidad falla: ABORTAR, no parar ni reiniciar.
#
# Uso: alterctl.sh {status|start|stop|restart|preflight}

set -euo pipefail

UNIT="alter.service"
WORKDIR="/home/balneario/alter"
ENVFILE="/home/balneario/alter/.env"
BIN="/home/balneario/alter/alter"
EXPECTED_BOT="AlterYouBot"
SYSTEMCTL="systemctl --user"

die() { echo "ABORT alterctl: $*" >&2; exit 1; }
info() { echo "alterctl: $*"; }

# envval — extrae el valor de CLAVE del .env de ALTER (nunca se imprime).
envval() {
    sed -n "s/^$1=//p" "$ENVFILE" | head -n1
}

# bot_username — nombre de usuario del bot para el token de ALTER (getMe).
# El token solo vive en variable de entorno local; jamás se loguea.
bot_username() {
    local token
    token="$(envval TELEGRAM_TOKEN)"
    [ -n "$token" ] || return 1
    curl -s -m 15 "https://api.telegram.org/bot${token}/getMe" \
        | python3 -c 'import json,sys
d=json.load(sys.stdin)
print(d.get("result",{}).get("username",""))'
}

# check_file — el .env exacto de ALTER existe y es legible.
check_envfile() {
    [ -f "$ENVFILE" ] && [ -r "$ENVFILE" ] || return 1
}

# check_bin — el binario canónico existe y es ejecutable.
check_bin() {
    [ -x "$BIN" ] || return 1
}

# check_unit_identity — la unit exacta con WorkingDirectory/ExecStart/EnvironmentFile esperados.
check_unit_identity() {
    $SYSTEMCTL show "$UNIT" -p WorkingDirectory --value | grep -qx "$WORKDIR" || return 1
    $SYSTEMCTL show "$UNIT" -p ExecStart --value | grep -q "path=$BIN" || return 1
    envf="$($SYSTEMCTL show "$UNIT" -p EnvironmentFile --value)"
    [ -z "$envf" ] || echo "$envf" | grep -q "EnvironmentFile=$ENVFILE\|$ENVFILE" || return 1
    return 0
}

# check_mainpid — MainPID obtenido SOLO de systemd; proceso == binario && cwd == workdir.
# Devuelve el PID en el file $1. 0/PID muerto/desviado => fallo.
check_mainpid() {
    local pid exe cwd exit_code=0
    pid="$($SYSTEMCTL show "$UNIT" -p MainPID --value)"
    [ "$pid" -gt 1 ] 2>/dev/null || return 1
    [ -d "/proc/$pid" ] || return 1
    exe="$(readlink -f "/proc/$pid/exe" 2>/dev/null || true)"
    [ "$exe" = "$BIN" ] || return 1
    cwd="$(readlink "/proc/$pid/cwd" 2>/dev/null || true)"
    [ "$cwd" = "$WORKDIR" ] || return 1
    echo "$pid" > "$1"
    return 0
}

# check_chatid — TELEGRAM_CHAT_ID del .env de ALTER, numérico no vacío.
check_chatid() {
    local chat
    chat="$(envval TELEGRAM_CHAT_ID)"
    [ -n "$chat" ] && [ "$chat" -eq "$chat" ] 2>/dev/null
}

# check_bot — Telegram getMe => EXACTAMENTE AlterYouBot (token nunca se imprime).
check_bot() {
    local uname
    uname="$(bot_username)" || return 1
    [ "$uname" = "$EXPECTED_BOT" ]
}

# preflight — verifica TODO antes de stop/restart/E2E; aborta en el primer fallo.
preflight() {
    local pidfile
    pidfile="$(mktemp)"
    trap 'rm -f "$pidfile"' RETURN

    info "preflight de" "$UNIT"
    check_envfile || die "identidad no verificada: .env de ALTER ($ENVFILE) no existe/ilegible"
    check_bin || die "identidad no verificada: binario canónico ($BIN) no existe/ejecutable"
    check_unit_identity || die "identidad no verificada: unit ($UNIT) con WorkingDirectory/ExecStart/EnvironmentFile inesperados"
    check_chatid || die "identidad no verificada: TELEGRAM_CHAT_ID ausente/no numérico en $ENVFILE"
    check_bot || die "identidad no verificada: Telegram getMe NO devolvió $EXPECTED_BOT (¿token equivocado? ¿bot de otro servicio?)"

    # MainPID: exigido para operaciones sobre un proceso en marcha.
    if check_mainpid "$pidfile"; then
        info "proceso verificado: PID $(cat "$pidfile") = $BIN (cwd=$WORKDIR)"
    else
        info "proceso: sin MainPID activo de $UNIT (estado controlado por systemd)"
    fi
    info "identidad de ALTER verificada OK (bot=$EXPECTED_BOT)"
}

require_running() {
    local pidfile
    pidfile="$(mktemp)"
    trap 'rm -f "$pidfile"' RETURN
    check_mainpid "$pidfile" || die "MainPID inactivo o no verificado para $UNIT"
}

case "${1:-}" in
    preflight)
        preflight
        ;;
    status)
        # status no muta nada: muestra estado; preflight ligero de identidad salvo MainPID.
        check_envfile || die "identidad no verificada: .env"
        check_bot || die "identidad no verificada: getMe != $EXPECTED_BOT"
        $SYSTEMCTL --no-pager status "$UNIT"
        ;;
    start)
        preflight
        $SYSTEMCTL start "$UNIT"
        sleep 1
        info "start: $UNIT MainPID=$($SYSTEMCTL show "$UNIT" -p MainPID --value)"
        ;;
    stop)
        # Orden del guard: 1) identidad completa de ALTER, 2) MainPID actual
        # verificado, 3) solo entonces systemctl stop. Cualquier fallo aborta.
        preflight
        require_running
        $SYSTEMCTL stop "$UNIT"
        info "stop: $UNIT detenida (MainPID 0)"
        ;;
    restart)
        # Orden del guard: 1) identidad completa de ALTER, 2) MainPID actual
        # verificado, 3) solo entonces systemctl restart. Cualquier fallo aborta.
        preflight
        require_running
        $SYSTEMCTL restart "$UNIT"
        sleep 1
        info "restart: $UNIT MainPID=$($SYSTEMCTL show "$UNIT" -p MainPID --value)"
        ;;
    *)
        echo "Uso: $0 {status|start|stop|restart|preflight}" >&2
        exit 2
        ;;
esac