"""Opt-in real network check. No login, API credentials or message requests."""
import asyncio
import os

from python_socks import ProxyError
from python_socks.async_.asyncio import Proxy
from telethon.client.telegrambaseclient import DEFAULT_IPV4_IP


async def main():
    proxy = Proxy.from_url(os.environ["HTTP_PROXY"])
    socket = await proxy.connect(dest_host=DEFAULT_IPV4_IP, dest_port=443, timeout=10)
    socket.close()
    try:
        socket = await proxy.connect(dest_host="1.1.1.1", dest_port=443, timeout=5)
    except ProxyError:
        pass
    else:
        socket.close()
        raise AssertionError("Non-Telegram egress allowed")
    try:
        reader, writer = await asyncio.wait_for(asyncio.open_connection(DEFAULT_IPV4_IP, 443), timeout=2)
    except (TimeoutError, OSError):
        pass
    else:
        writer.close()
        await writer.wait_closed()
        raise AssertionError("Direct TCP bypass allowed")
    print("PASS: Telegram CONNECT allowed; other IP and direct TCP denied. No account login performed.")


if __name__ == "__main__":
    asyncio.run(main())
