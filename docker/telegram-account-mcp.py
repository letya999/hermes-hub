"""Telegram user-account MCP, backed by the owner's MTProto session.

This is deliberately read-only by default, but it is not a Bot API feed: the
same user session can enumerate dialogs, page backwards through old history,
and search Telegram's server-side history. Credentials are supplied per call by
Go under an exclusive connection lock.
"""
from contextlib import asynccontextmanager
from pathlib import Path
import os
from urllib.parse import urlsplit

from mcp.server.fastmcp import FastMCP
from telethon import TelegramClient
from telethon.sessions import StringSession
from telethon.tl.types import User

mcp = FastMCP("ToolHub Telegram account")


def network_proxy():
    url = urlsplit(os.environ.get("HTTP_PROXY", ""))
    if url.scheme != "http" or not url.hostname or not url.port or url.username or url.password or url.path not in ("", "/") or url.query or url.fragment:
        raise ValueError("Local HTTP CONNECT proxy required")
    return {"proxy_type": "http", "addr": url.hostname, "port": url.port}


def credentials():
    path = Path("/run/connector/credentials.env")
    if path.is_symlink() or path.stat().st_size > 32768:
        raise ValueError("Invalid credential input")
    values = dict(line.split("=", 1) for line in path.read_text().splitlines())
    required = {"TELEGRAM_API_ID", "TELEGRAM_API_HASH", "TELEGRAM_SESSION_STRING", "TELEGRAM_ACCOUNT_ID"}
    if not required <= values.keys() or "TELEGRAM_BOT_TOKEN" in values:
        raise ValueError("Personal account credentials required")
    return values


@asynccontextmanager
async def account():
    values = credentials()
    client = TelegramClient(StringSession(values["TELEGRAM_SESSION_STRING"]),
                            int(values["TELEGRAM_API_ID"]), values["TELEGRAM_API_HASH"],
                            device_model="hermes-hub ToolHub", receive_updates=False,
                            request_retries=0, connection_retries=1, flood_sleep_threshold=0,
                            proxy=network_proxy())
    try:
        await client.connect()
        me = await client.get_me()
        if me is None or me.bot or str(me.id) != values["TELEGRAM_ACCOUNT_ID"]:
            raise ValueError("Account mismatch")
        yield client, values
    finally:
        await client.disconnect()


async def peer(client, peer_id):
    if type(peer_id) is not int or peer_id == 0:
        raise ValueError("Non-zero Telegram peer ID required")
    entity = await client.get_entity(peer_id)
    if isinstance(entity, User) and entity.bot:
        raise ValueError("Bot peers are disabled")
    return entity


def message_record(message):
    date = getattr(message, "date", None)
    return {"message_id": message.id, "date": date.isoformat() if date else None,
            "sender_id": getattr(message, "sender_id", None), "text": (getattr(message, "message", None) or "")[:4096],
            "has_media": getattr(message, "media", None) is not None, "reply_to": getattr(message, "reply_to_msg_id", None)}


def history_result(peer_id, messages):
    records = [message_record(message) for message in messages]
    return {"peer_id": peer_id, "messages": records,
            "next_before_message_id": records[-1]["message_id"] if records else None}


def message_id(value):
    if type(value) is not int or value <= 0:
        raise ValueError("Positive message ID required")
    return value


def mutation(values):
    if values.get("TELEGRAM_WRITE") != "true":
        raise ValueError("Write grant required")


@mcp.tool()
async def get_account() -> dict:
    async with account() as (client, values):
        return {"account_id": values["TELEGRAM_ACCOUNT_ID"]}


@mcp.tool()
async def list_dialogs(limit: int = 50, offset_id: int = 0) -> dict:
    if type(limit) is not int or not 1 <= limit <= 100:
        raise ValueError("Limit must be 1..100")
    if type(offset_id) is not int or offset_id < 0:
        raise ValueError("Dialog cursor must be a non-negative integer")
    async with account() as (client, values):
        dialogs = await client.get_dialogs(limit=limit, offset_id=offset_id)
        rows = []
        next_offset_id = None
        for dialog in dialogs:
            entity = dialog.entity
            if isinstance(entity, User) and entity.bot:
                continue
            rows.append({"peer_id": entity.id, "name": (dialog.name or "")[:256],
                         "kind": "user" if isinstance(entity, User) else "channel_or_group"})
            top_message = getattr(getattr(dialog, "dialog", None), "top_message", None)
            if top_message:
                next_offset_id = top_message
        return {"dialogs": rows, "next_offset_id": next_offset_id}


@mcp.tool()
async def get_messages(peer_id: int, limit: int = 50, before_message_id: int = 0,
                       after_message_id: int = 0, query: str = "") -> dict:
    if type(limit) is not int or not 1 <= limit <= 100:
        raise ValueError("Limit must be 1..100")
    if type(before_message_id) is not int or before_message_id < 0 or type(after_message_id) is not int or after_message_id < 0:
        raise ValueError("Message cursors must be non-negative integers")
    if before_message_id and after_message_id:
        raise ValueError("Use before_message_id or after_message_id, not both")
    if type(query) is not str or len(query) > 256:
        raise ValueError("Query must be at most 256 characters")
    async with account() as (client, values):
        entity = await peer(client, peer_id)
        messages = await client.get_messages(entity, limit=limit, offset_id=before_message_id,
                                             min_id=after_message_id, search=query or None)
        return history_result(peer_id, messages)


@mcp.tool()
async def search_messages(query: str, peer_id: int = 0, limit: int = 50,
                          before_message_id: int = 0) -> dict:
    """Search the owner's live Telegram history, globally or in one peer."""
    if not query or len(query) > 256:
        raise ValueError("Query must be 1..256 characters")
    if type(limit) is not int or not 1 <= limit <= 100 or type(before_message_id) is not int or before_message_id < 0:
        raise ValueError("Invalid limit or history cursor")
    async with account() as (client, values):
        entity = await peer(client, peer_id) if peer_id else None
        messages = await client.get_messages(entity, limit=limit, offset_id=before_message_id,
                                             search=query)
        return history_result(peer_id or None, messages)


@mcp.tool()
async def get_chat_info(peer_id: int) -> dict:
    async with account() as (client, values):
        entity = await peer(client, peer_id)
        kind = "user" if isinstance(entity, User) else "channel_or_group"
        return {"peer_id": entity.id, "kind": kind, "name": (getattr(entity, "title", None) or
                getattr(entity, "first_name", None) or "")[:256], "username": getattr(entity, "username", None)}


async def send(peer_id, text, reply_to=None):
    if not text or len(text) > 4096:
        raise ValueError("Text must be 1..4096 characters")
    async with account() as (client, values):
        mutation(values)
        entity = await peer(client, peer_id)
        if reply_to is not None:
            message_id(reply_to)
            target = await client.get_messages(entity, ids=reply_to)
            if target is None or target.chat_id != entity.id:
                raise ValueError("Reply target not found")
        sent = await client.send_message(entity, text, reply_to=reply_to, parse_mode=None, link_preview=False)
        return {"peer_id": entity.id, "message_id": message_id(sent.id),
                "receipt": f"{values['TELEGRAM_ACCOUNT_ID']}:{entity.id}:{sent.id}"}


@mcp.tool()
async def send_message(peer_id: int, text: str) -> dict:
    return await send(peer_id, text)


@mcp.tool()
async def reply_message(peer_id: int, message_id: int, text: str) -> dict:
    return await send(peer_id, text, message_id)


@mcp.tool()
async def delete_message(peer_id: int, target_id: int) -> dict:
    message_id(target_id)
    async with account() as (client, values):
        mutation(values)
        entity = await peer(client, peer_id)
        target = await client.get_messages(entity, ids=target_id)
        if target is None or target.chat_id != entity.id or not target.out:
            raise ValueError("Only own direct messages can be deleted")
        result = await client.delete_messages(entity, [target_id], revoke=True)
        if len(result) != 1 or result[0].pts_count <= 0:
            raise ValueError("No provider deletion acknowledgement")
        return {"peer_id": entity.id, "message_id": target_id, "pts": result[0].pts,
                "receipt": f"{values['TELEGRAM_ACCOUNT_ID']}:{entity.id}:{target_id}:pts:{result[0].pts}"}


if __name__ == "__main__":
    if os.environ.get("HUB_TELEGRAM_WRITE") != "true":
        for name in ("send_message", "reply_message", "delete_message"):
            mcp.remove_tool(name)
    # ToolHive wraps this stdio server and owns network/resource enforcement.
    mcp.run(transport="stdio")
