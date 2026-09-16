"""Run with the pinned Telegram virtualenv; no real account or network needed."""
import importlib.util
from contextlib import asynccontextmanager
from types import SimpleNamespace
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("adapter", __file__.replace("test_telegram_account.py", "telegram-account-mcp.py"))
adapter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(adapter)


class Client:
    async def get_entity(self, peer_id):
        return adapter.User(id=peer_id, bot=False)

    async def send_message(self, entity, text, **kwargs):
        self.sent = (entity.id, text, kwargs)
        return SimpleNamespace(id=42)

    async def get_messages(self, entity, **kwargs):
        self.last_message_args = kwargs
        if "ids" in kwargs:
            return SimpleNamespace(id=kwargs["ids"], out=True, chat_id=entity.id)
        return [SimpleNamespace(id=1, message="text", date=None, sender_id=10, media=None)]

    async def delete_messages(self, entity, ids, **kwargs):
        return [SimpleNamespace(pts=123, pts_count=1)]

    async def get_dialogs(self, **kwargs):
        return [SimpleNamespace(entity=adapter.User(id=7, bot=False), name="direct"),
                SimpleNamespace(entity=SimpleNamespace(id=-1), name="group")]


class AdapterTest(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.client = Client()
        self.values = {"TELEGRAM_ACCOUNT_ID": "10", "TELEGRAM_WRITE": "true"}
        self.original = adapter.account

        @asynccontextmanager
        async def account():
            yield self.client, self.values
        adapter.account = account

    async def asyncTearDown(self):
        adapter.account = self.original

    async def test_reads_and_receipts(self):
        self.assertEqual(await adapter.get_account(), {"account_id": "10"})
        self.assertEqual((await adapter.list_dialogs())["dialogs"], [{"peer_id": 7, "name": "direct", "kind": "user"}, {"peer_id": -1, "name": "group", "kind": "channel_or_group"}])
        self.assertEqual((await adapter.get_messages(7))["messages"][0]["message_id"], 1)
        history = await adapter.get_messages(-1007, limit=10, before_message_id=99)
        self.assertEqual(history["next_before_message_id"], 1)
        self.assertEqual(self.client.last_message_args["offset_id"], 99)
        await adapter.search_messages("invoice", peer_id=-1007)
        self.assertEqual(self.client.last_message_args["search"], "invoice")
        self.assertEqual((await adapter.send_message(7, "hello"))["receipt"], "10:7:42")
        self.assertEqual((await adapter.reply_message(7, 1, "reply"))["message_id"], 42)
        self.assertEqual(self.client.sent[2]["reply_to"], 1)
        self.assertEqual((await adapter.delete_message(7, 1))["receipt"], "10:7:1:pts:123")
        names = {tool.name for tool in await adapter.mcp.list_tools()}
        self.assertEqual(names, {"get_account", "list_dialogs", "get_messages", "search_messages", "get_chat_info", "send_message", "reply_message", "delete_message"})

    async def test_denial(self):
        for value in (0, True, "7"):
            with self.assertRaises(ValueError):
                await adapter.get_messages(value)
        for limit in (0, 101, True):
            with self.assertRaises(ValueError):
                await adapter.list_dialogs(limit)
        with self.assertRaises(ValueError):
            await adapter.get_messages(7, before_message_id=4, after_message_id=3)
        with self.assertRaises(ValueError):
            await adapter.search_messages("")
        for text in ("", "x" * 4097):
            with self.assertRaises(ValueError):
                await adapter.send_message(7, text)
        self.values["TELEGRAM_WRITE"] = "false"
        with self.assertRaises(ValueError):
            await adapter.send_message(7, "no")
        with self.assertRaises(ValueError):
            await adapter.delete_message(7, 1)

    async def test_cross_peer_target_is_denied(self):
        async def wrong_target(entity, **kwargs):
            return SimpleNamespace(id=1, out=True, chat_id=999)
        self.client.get_messages = wrong_target
        with self.assertRaises(ValueError):
            await adapter.reply_message(7, 1, "no")
        with self.assertRaises(ValueError):
            await adapter.delete_message(7, 1)


class AccountBindingTest(unittest.IsolatedAsyncioTestCase):
    async def test_session_identity_and_disconnect(self):
        values = {"TELEGRAM_API_ID": "1", "TELEGRAM_API_HASH": "fixture",
                  "TELEGRAM_SESSION_STRING": "fixture", "TELEGRAM_ACCOUNT_ID": "10"}
        for me in (SimpleNamespace(id=10, bot=False), SimpleNamespace(id=99, bot=False),
                   SimpleNamespace(id=10, bot=True), None):
            class Session:
                disconnected = False

                async def connect(self):
                    pass

                async def get_me(self):
                    return me

                async def disconnect(self):
                    self.disconnected = True

            client = Session()
            with patch.object(adapter, "credentials", return_value=values), \
                 patch.object(adapter, "network_proxy", return_value=None), \
                 patch.object(adapter, "StringSession", return_value=None), \
                 patch.object(adapter, "TelegramClient", return_value=client):
                if me is not None and me.id == 10 and not me.bot:
                    self.assertEqual((await adapter.get_account())["account_id"], "10")
                else:
                    with self.assertRaises(ValueError):
                        await adapter.get_account()
            self.assertTrue(client.disconnected)

    def test_proxy_validation(self):
        for value in ("", "socks5://localhost:1080", "http://user:pass@localhost:3128", "http://localhost:3128/path", "http://localhost"):
            with patch.dict(adapter.os.environ, {"HTTP_PROXY": value}), self.assertRaises(ValueError):
                adapter.network_proxy()
        with patch.dict(adapter.os.environ, {"HTTP_PROXY": "http://127.0.0.1:3128"}):
            self.assertEqual(adapter.network_proxy(), {"proxy_type": "http", "addr": "127.0.0.1", "port": 3128})


if __name__ == "__main__":
    unittest.main()
