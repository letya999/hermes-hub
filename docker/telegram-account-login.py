"""User-operated local login; never prints a session or sends messages."""
import asyncio
import getpass
import os
from pathlib import Path
import re
import runpy

from telethon import TelegramClient, errors
from telethon.sessions import StringSession

_T = {
    "api_id": {
        "en": "API ID (hidden input): ",
        "ru": "API ID (ввод скрыт): ",
        "kk": "API ID (жасырын енгізу): ",
        "es": "API ID (entrada oculta): ",
    },
    "api_hash": {
        "en": "API hash (hidden input): ",
        "ru": "API hash (ввод скрыт): ",
        "kk": "API hash (жасырын енгізу): ",
        "es": "API hash (entrada oculta): ",
    },
    "err.api": {
        "en": "Check the API ID and API hash from my.telegram.org/apps",
        "ru": "Проверьте API ID и API hash из my.telegram.org/apps",
        "kk": "my.telegram.org/apps-тегі API ID және API hash-ты тексеріңіз",
        "es": "Revisa el API ID y el API hash de my.telegram.org/apps",
    },
    "phone": {
        "en": "Telegram number with +country code (hidden input): ",
        "ru": "Номер Telegram с +кодом страны (ввод скрыт): ",
        "kk": "+ел кодымен Telegram нөмірі (жасырын енгізу): ",
        "es": "Número de Telegram con +código de país (entrada oculta): ",
    },
    "err.phone": {
        "en": "Personal number with +country code; a bot token does not work",
        "ru": "Личный номер с +кодом страны; bot token не подходит",
        "kk": "+ел кодымен жеке нөмір; bot token жарамайды",
        "es": "Número personal con +código de país; un token de bot no vale",
    },
    "code": {
        "en": "Code from Telegram (hidden input): ",
        "ru": "Код из Telegram (ввод скрыт): ",
        "kk": "Telegram-дағы код (жасырын енгізу): ",
        "es": "Código de Telegram (entrada oculta): ",
    },
    "password": {
        "en": "Two-step verification password (hidden input): ",
        "ru": "Пароль двухэтапной проверки (ввод скрыт): ",
        "kk": "Екі сатылы тексеру құпиясөзі (жасырын енгізу): ",
        "es": "Contraseña de verificación en dos pasos (entrada oculta): ",
    },
    "err.bot": {
        "en": "A personal account is required, not a bot",
        "ru": "Требуется личный аккаунт, не бот",
        "kk": "Жеке аккаунт қажет, бот емес",
        "es": "Se requiere una cuenta personal, no un bot",
    },
    "done": {
        "en": "Signed in. The session is stored locally; no need to copy it.",
        "ru": "Вход выполнен. Сессия сохранена локально; копировать её не нужно.",
        "kk": "Кіру орындалды. Сессия жергілікті сақталды; көшіру керек емес.",
        "es": "Sesión iniciada. La sesión se guarda localmente; no hace falta copiarla.",
    },
    "fail": {
        "en": "Sign-in did not finish. Check your input and Telegram messages; if attempts are limited, wait.",
        "ru": "Вход не завершён. Проверьте ввод и сообщения Telegram; при ограничении попыток подождите.",
        "kk": "Кіру аяқталмады. Енгізуді және Telegram хабарларын тексеріңіз; әрекеттер шектелсе, күтіңіз.",
        "es": "El inicio de sesión no terminó. Revisa la entrada y los mensajes de Telegram; si hay límite de intentos, espera.",
    },
}


def _t(key):
    # The ps1 wrapper passes a normalized operator language as HUB_LANG.
    return _T[key].get(os.environ.get("HUB_LANG", ""), _T[key]["en"])


def save_session(directory, api_id, api_hash, session, account_id):
    directory = Path(directory)
    if directory.is_symlink() or not directory.is_dir():
        raise ValueError("Private output directory required")
    path = directory / "telegram-session.env"
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "w") as output:
        output.write(f"TELEGRAM_API_ID={api_id}\nTELEGRAM_API_HASH={api_hash}\nTELEGRAM_SESSION_STRING={session}\n")
        output.flush()
        os.fsync(output.fileno())
    with (directory / "telegram-account-id.txt").open("x") as output:
        output.write(str(account_id))


async def main():
    api_id = getpass.getpass(_t("api_id")).strip()
    api_hash = getpass.getpass(_t("api_hash")).strip()
    if not api_id.isdecimal() or not 0 < int(api_id) < 2**31 or re.fullmatch(r"[0-9a-fA-F]{32}", api_hash) is None:
        raise ValueError(_t("err.api"))
    client = TelegramClient(StringSession(), int(api_id), api_hash,
                            device_model="hermes-hub ToolHub", receive_updates=False,
                            proxy=runpy.run_path("/opt/hub/telegram-account-mcp.py")["network_proxy"]())
    try:
        phone = getpass.getpass(_t("phone")).strip()
        if re.fullmatch(r"\+[0-9]{7,15}", phone) is None:
            raise ValueError(_t("err.phone"))
        await client.connect()
        await client.send_code_request(phone)
        code = getpass.getpass(_t("code")).strip()
        try:
            await client.sign_in(phone=phone, code=code)
        except errors.SessionPasswordNeededError:
            await client.sign_in(password=getpass.getpass(_t("password")))
        me = await client.get_me()
        if me is None or me.bot:
            raise ValueError(_t("err.bot"))
        save_session("/login-output", api_id, api_hash, client.session.save(), me.id)
        print(_t("done"))
    finally:
        await client.disconnect()


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except Exception:
        # Provider exceptions can include phone numbers. Keep the local output generic.
        print(_t("fail"))
        raise SystemExit(1) from None
