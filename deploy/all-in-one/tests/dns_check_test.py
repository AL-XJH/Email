#!/usr/bin/env python3
"""Exercise real DNS packets, invalid responses and a missing resolver."""
import importlib.util
from pathlib import Path
import socket
import struct
import threading
import unittest

spec = importlib.util.spec_from_file_location("dns_check", Path(__file__).resolve().parents[1] / "dns-check.py")
assert spec is not None and spec.loader is not None
check = importlib.util.module_from_spec(spec)
spec.loader.exec_module(check)


class DNSCheckTest(unittest.TestCase):
    def reply(self, flags=0x81A0, answers=1, wrong_id=False):
        sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        sock.bind(("127.0.0.1", 0))
        sock.settimeout(3)
        address = f"127.0.0.1:{sock.getsockname()[1]}"

        def serve():
            try:
                data, peer = sock.recvfrom(4096)
                ident = struct.unpack("!H", data[:2])[0]
                if wrong_id:
                    ident ^= 1
                # Root NS -> a.root-servers.net; full RR, not just a header.
                name = b"\x01a\x0croot-servers\x03net\0"
                rr = b"\xc0\x0c" + struct.pack("!HHIH", 2, 1, 60, len(name)) + name
                sock.sendto(struct.pack("!6H", ident, flags, 1, answers, 0, 0) + data[12:17] + (rr if answers else b""), peer)
            finally:
                sock.close()

        thread = threading.Thread(target=serve)
        thread.start()
        self.addCleanup(thread.join, 4)
        return address

    def test_validated_root_answer(self):
        check.probe(self.reply())

    def test_dns_error_rejected(self):
        with self.assertRaises(ValueError):
            check.probe(self.reply(flags=0x81A2))

    def test_unvalidated_answer_rejected(self):
        with self.assertRaises(ValueError):
            check.probe(self.reply(flags=0x8180))

    def test_empty_answer_rejected(self):
        with self.assertRaises(ValueError):
            check.probe(self.reply(answers=0))

    def test_wrong_transaction_rejected(self):
        with self.assertRaises(ValueError):
            check.probe(self.reply(wrong_id=True))

    def test_dead_resolver_rejected(self):
        sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        sock.bind(("127.0.0.1", 0))
        address = f"127.0.0.1:{sock.getsockname()[1]}"
        sock.close()
        with self.assertRaises(OSError):
            check.probe(address)


if __name__ == "__main__":
    unittest.main()
