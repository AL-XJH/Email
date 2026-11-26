#!/usr/bin/env python3
"""Bounded DNSSEC-validating DNS probe; no Python dependencies or shell parsing."""
import ipaddress
import os
import secrets
import socket
import struct
import sys

LOCAL = "127.0.0.1:5353"


def endpoint(value):
    if value.startswith("["):
        host, separator, port = value[1:].partition("]:")
    else:
        host, separator, port = value.rpartition(":")
    if not separator or not host or not port.isdecimal() or not 0 < int(port) < 65536:
        raise ValueError("expected a single numeric recursive DNS address:port")
    address = ipaddress.ip_address(host)
    return socket.AF_INET6 if address.version == 6 else socket.AF_INET, host, int(port)


def probe(value):
    family, host, port = endpoint(value)
    ident = secrets.randbelow(65536)
    # Root NS is independent of any third-party probe domain. DO requests DNSSEC;
    # AD plus a nonempty answer proves that a validating resolver answered.
    question = b"\0" + struct.pack("!HH", 2, 1)
    edns = b"\0" + struct.pack("!HHIH", 41, 1232, 0x8000, 0)
    packet = struct.pack("!6H", ident, 0x0100, 1, 0, 0, 1) + question + edns
    with socket.socket(family, socket.SOCK_DGRAM) as sock:
        sock.settimeout(1.25)
        sock.connect((host, port))
        sock.send(packet)
        data = sock.recv(16384)
    if len(data) < 12 + len(question):
        raise ValueError("truncated DNS reply")
    reply_id, flags, questions, answers, _, _ = struct.unpack("!6H", data[:12])
    if (reply_id != ident or flags & 0x8000 == 0 or flags & 0x0200
            or flags & 0x000F or flags & 0x0020 == 0
            or questions != 1 or answers == 0 or data[12:12 + len(question)] != question):
        raise ValueError("recursive DNS returned no validated root NS answer")


def main():
    configured = os.environ.get("LANQIN_RSPAMD_DNS", LOCAL).strip() or LOCAL
    # An override must not hide failure of the bundled resolver, or vice versa.
    for value in dict.fromkeys((LOCAL, configured)):
        try:
            probe(value)
        except (OSError, ValueError) as error:
            print(f"recursive DNS health failed for {value}: {error}", file=sys.stderr)
            return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
