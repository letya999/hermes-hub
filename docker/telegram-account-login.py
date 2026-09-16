"""User-operated local login; never prints a session or sends messages."""
import asyncio
import getpass
import os
from pathlib import Path
import re
import runpy

from telethon import TelegramClient, errors
from telethon.sessions import StringSession


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
    api_id = getpass.getpass("API ID (ввод скрыт): ").strip()
    api_hash = getpass.getpass("API hash (ввод скрыт): ").strip()
    if not api_id.isdecimal() or not 0 < int(api_id) < 2**31 or re.fullmatch(r"[0-9a-fA-F]{32}", api_hash) is None:
        raise ValueError("Проверьте API ID и API hash из my.telegram.org/apps")
    client = TelegramClient(StringSession(), int(api_id), api_hash,
                            device_model="hermes-hub ToolHub", receive_updates=False,
                            proxy=runpy.run_path("/opt/hub/telegram-account-mcp.py")["network_proxy"]())
    try:
        phone = getpass.getpass("Номер Telegram с +кодом страны (ввод скрыт): ").strip()
        if re.fullmatch(r"\+[0-9]{7,15}", phone) is None:
            raise ValueError("Личный номер с +кодом страны; bot token не подходит")
        await client.connect()
        await client.send_code_request(phone)
        code = getpass.getpass("Код из Telegram (ввод скрыт): ").strip()
        try:
            await client.sign_in(phone=phone, code=code)
        except errors.SessionPasswordNeededError:
            await client.sign_in(password=getpass.getpass("Пароль двухэтапной проверки (ввод скрыт): "))
        me = await client.get_me()
        if me is None or me.bot:
            raise ValueError("Требуется личный аккаунт, не бот")
        save_session("/login-output", api_id, api_hash, client.session.save(), me.id)
        print("Вход выполнен. Сессия сохранена локально; копировать её не нужно.")
    finally:
        await client.disconnect()


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except Exception:
        # Provider exceptions can include phone numbers. Keep the local output generic.
        print("Вход не завершён. Проверьте ввод и сообщения Telegram; при ограничении попыток подождите.")
        raise SystemExit(1) from None
