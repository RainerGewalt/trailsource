#!/usr/bin/env python3
"""Minimal TCP relay for interop and reconnect tests.

    tcp_relay.py <listen_port> <target_host> <target_port> [--mode half-open|drop-publish]

Stop it (SIGTERM) to cut a session; start it again to let the adapter
reconnect, without touching the server.

SIGUSR1 switches a fault on, SIGUSR2 off again (docs/SCENARIOS.md):

    --mode half-open (default)
        Every connection, open and new, stays open but nothing is forwarded:
        the client's bytes are accepted and dropped, no answer ever comes
        back, no FIN or RST is sent. SIGUSR2 closes the silenced connections
        and forwards new ones again.
    --mode drop-publish
        Only OPC UA PublishResponse messages from the server are dropped;
        every other message, Read responses included, is forwarded. The
        session stays healthy while the subscription goes silent. Works on
        unsecured channels (security mode None), where the message type is
        readable. SIGUSR2 forwards publish responses again.

Listens on 127.0.0.1 only. Stdlib only, Python 3.7+. See docs/INTEROP.md.
"""
import argparse
import asyncio
import signal
import struct

PUBLISH_RESPONSE = 829  # PublishResponse_Encoding_DefaultBinary

state = {"fault": False, "silenced": set(), "dropped": 0}


def message_type(chunk):
    """Encoding NodeId of the body of a symmetric MSG chunk (security None):
    8 bytes header, SecureChannelId, TokenId, SequenceNumber, RequestId."""
    body = chunk[24:]
    if len(body) >= 4 and body[0] == 0x01:  # four-byte NodeId: ns byte, uint16 id
        return struct.unpack_from("<H", body, 2)[0]
    if len(body) >= 7 and body[0] == 0x02:  # numeric NodeId: uint16 ns, uint32 id
        return struct.unpack_from("<I", body, 3)[0]
    return None


async def pipe(reader, writer, conn):
    try:
        while True:
            data = await reader.read(65536)
            if not data:
                break
            if state["fault"] and MODE == "half-open":
                state["silenced"].add(conn)
                continue  # accepted and dropped: the peer waits forever
            writer.write(data)
            await writer.drain()
    except (ConnectionError, asyncio.CancelledError):
        pass
    finally:
        writer.close()


async def pipe_chunks(reader, writer):
    """Server to client in drop-publish mode: forward chunk by chunk and drop
    PublishResponse chunks (and the continuation chunks of the same request)
    while the fault is on."""
    buf = b""
    dropping = set()  # request ids of publish responses being dropped
    try:
        while True:
            data = await reader.read(65536)
            if not data:
                break
            buf += data
            while len(buf) >= 8:
                size = struct.unpack_from("<I", buf, 4)[0]
                if len(buf) < size:
                    break
                chunk, buf = buf[:size], buf[size:]
                if state["fault"] and chunk[:3] == b"MSG" and len(chunk) >= 24:
                    request_id = struct.unpack_from("<I", chunk, 20)[0]
                    if request_id in dropping or message_type(chunk) == PUBLISH_RESPONSE:
                        dropping.add(request_id)
                        if chunk[3:4] in (b"F", b"A"):
                            dropping.discard(request_id)
                        state["dropped"] += 1
                        continue
                writer.write(chunk)
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
        if MODE == "drop-publish":
            await asyncio.gather(pipe(client_reader, server_writer, conn), pipe_chunks(server_reader, client_writer))
        else:
            await asyncio.gather(pipe(client_reader, server_writer, conn), pipe(server_reader, client_writer, conn))

    def fault_on():
        state["fault"] = True
        what = "nothing is forwarded" if MODE == "half-open" else "publish responses are dropped"
        print("relay: %s on, %s" % (MODE, what), flush=True)

    def fault_off():
        state["fault"] = False
        for client_writer, server_writer in state["silenced"]:
            client_writer.close()
            server_writer.close()
        print("relay: %s off, closed %d silenced connection(s), dropped %d publish chunk(s)"
              % (MODE, len(state["silenced"]), state["dropped"]), flush=True)
        state["silenced"].clear()

    server = await asyncio.start_server(handle, "127.0.0.1", listen_port)
    print("relay: 127.0.0.1:%d -> %s:%d (mode %s)" % (listen_port, host, port, MODE), flush=True)
    stop = asyncio.Event()
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGTERM, signal.SIGINT):
        loop.add_signal_handler(sig, stop.set)
    loop.add_signal_handler(signal.SIGUSR1, fault_on)
    loop.add_signal_handler(signal.SIGUSR2, fault_off)
    async with server:
        await stop.wait()


if __name__ == "__main__":
    ap = argparse.ArgumentParser(usage=__doc__)
    ap.add_argument("listen_port", type=int)
    ap.add_argument("target_host")
    ap.add_argument("target_port", type=int)
    ap.add_argument("--mode", choices=("half-open", "drop-publish"), default="half-open")
    args = ap.parse_args()
    MODE = args.mode
    asyncio.run(main(args.listen_port, args.target_host, args.target_port))
