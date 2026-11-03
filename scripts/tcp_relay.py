#!/usr/bin/env python3
"""Minimal TCP relay for interop and reconnect tests.

    tcp_relay.py <listen_port> <target_host> <target_port>

Stop it (SIGTERM) to cut a session; start it again to let the adapter
reconnect, without touching the server.

Half-open mode, for the half-open scenario (docs/SCENARIOS.md):
    SIGUSR1  every connection, open and new, stays open but nothing is
             forwarded any more: the client's bytes are accepted and dropped,
             no answer ever comes back, no FIN or RST is sent
    SIGUSR2  back to normal: the connections that went silent are closed,
             new connections are forwarded again

Listens on 127.0.0.1 only. Stdlib only, Python 3.7+. See docs/INTEROP.md.
"""
import asyncio
import signal
import sys

state = {"blackhole": False, "silenced": set()}


async def pipe(reader, writer, conn):
    try:
        while True:
            data = await reader.read(65536)
            if not data:
                break
            if state["blackhole"]:
                state["silenced"].add(conn)
                continue  # accepted and dropped: the peer waits forever
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
        conn = (client_writer, server_writer)
        await asyncio.gather(pipe(client_reader, server_writer, conn), pipe(server_reader, client_writer, conn))

    def blackhole_on():
        state["blackhole"] = True
        print("relay: half-open, nothing is forwarded", flush=True)

    def blackhole_off():
        state["blackhole"] = False
        for client_writer, server_writer in state["silenced"]:
            client_writer.close()
            server_writer.close()
        print("relay: forwarding again, closed %d silenced connection(s)" % len(state["silenced"]), flush=True)
        state["silenced"].clear()

    server = await asyncio.start_server(handle, "127.0.0.1", listen_port)
    print("relay: 127.0.0.1:%d -> %s:%d" % (listen_port, host, port), flush=True)
    stop = asyncio.Event()
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGTERM, signal.SIGINT):
        loop.add_signal_handler(sig, stop.set)
    loop.add_signal_handler(signal.SIGUSR1, blackhole_on)
    loop.add_signal_handler(signal.SIGUSR2, blackhole_off)
    async with server:
        await stop.wait()


if __name__ == "__main__":
    if len(sys.argv) != 4:
        sys.exit(__doc__)
    asyncio.run(main(int(sys.argv[1]), sys.argv[2], int(sys.argv[3])))
