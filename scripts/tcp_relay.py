#!/usr/bin/env python3
"""Minimal TCP relay for interop tests: stop it to cut a session, start it
again to let the adapter reconnect, without touching the server.

    tcp_relay.py <listen_port> <target_host> <target_port>

Listens on 127.0.0.1 only. Stdlib only, Python 3.7+. See docs/INTEROP.md.
"""
import asyncio
import signal
import sys


async def pipe(reader, writer):
    try:
        while True:
            data = await reader.read(65536)
            if not data:
                break
            writer.write(data)
            await writer.drain()
    except (ConnectionError, asyncio.CancelledError):
        pass
    finally:
        writer.close()


async def main(listen_port, host, port):
    async def handle(client_reader, client_writer):
        try:
            server_reader, server_writer = await asyncio.open_connection(host, port)
        except OSError as exc:
            print("relay: upstream %s:%d unreachable: %s" % (host, port, exc), flush=True)
            client_writer.close()
            return
        await asyncio.gather(pipe(client_reader, server_writer), pipe(server_reader, client_writer))

    server = await asyncio.start_server(handle, "127.0.0.1", listen_port)
    print("relay: 127.0.0.1:%d -> %s:%d" % (listen_port, host, port), flush=True)
    stop = asyncio.Event()
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGTERM, signal.SIGINT):
        loop.add_signal_handler(sig, stop.set)
    async with server:
        await stop.wait()


if __name__ == "__main__":
    if len(sys.argv) != 4:
        sys.exit(__doc__)
    asyncio.run(main(int(sys.argv[1]), sys.argv[2], int(sys.argv[3])))
