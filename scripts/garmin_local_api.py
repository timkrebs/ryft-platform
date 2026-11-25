"""Ephemeral LAN endpoint for the signed RYFT Debug build."""

import hashlib
import ipaddress
import json
import math
import secrets
import ssl
import subprocess
import sys
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
from tempfile import TemporaryDirectory
from time import monotonic


PORT = 8785
PATH = "/v1/local/garmin/activities"
LIFETIME = 30 * 60
SNAPSHOT_TTL = 15
HANDSHAKE_TIMEOUT = 5
LAN_RANGES = [ipaddress.ip_network(block) for block in ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16")]


def lan_ip(value):
    address = ipaddress.IPv4Address(value)
    if not any(address in network for network in LAN_RANGES):
        raise ValueError("Use your Mac's private Wi-Fi IPv4 address.")
    return str(address)


def numeric(value):
    if type(value) in (int, float) and 0 <= value <= 10_000_000 and math.isfinite(value):
        return value
    return None


def snapshot(records):
    activities = []
    for record in records[:5]:
        if not isinstance(record, dict):
            continue
        kind = record.get("activityType")
        sport = kind.get("typeKey") if isinstance(kind, dict) else None
        start = record.get("startTimeLocal")
        activities.append({
            "sport": sport[:48] if isinstance(sport, str) else None,
            "started_at_local": start[:32] if isinstance(start, str) else None,
            "duration_seconds": numeric(record.get("duration")),
            "distance_meters": numeric(record.get("distance")),
        })
    return {"provider": "garmin", "fetched_at": datetime.now(timezone.utc).isoformat(), "activities": activities}


def tls(directory, address):
    cert, key = directory / "server.pem", directory / "server-key.pem"
    config = directory / "openssl.cnf"
    config.write_text(
        "[req]\nprompt = no\ndistinguished_name = dn\nx509_extensions = server\n"
        "[dn]\nCN = RYFT Garmin Local Test\n[server]\n"
        "basicConstraints = critical,CA:FALSE\nkeyUsage = critical,digitalSignature,keyEncipherment\n"
        f"extendedKeyUsage = serverAuth\nsubjectAltName = IP:{address}\n",
        encoding="utf-8",
    )
    try:
        subprocess.run(
            ["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1", "-sha256",
             "-config", str(config), "-keyout", str(key), "-out", str(cert)],
            check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=20,
        )
    except (OSError, subprocess.SubprocessError):
        raise ValueError("Could not create the local certificate. Check that openssl is installed.") from None
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.minimum_version = ssl.TLSVersion.TLSv1_2
    context.load_cert_chain(cert, key)
    der = ssl.PEM_cert_to_DER_cert(cert.read_text(encoding="ascii"))
    return context, hashlib.sha256(der).hexdigest()


def serve(address, fetch, *, provider_error):
    address = lan_ip(address)
    pairing_token = secrets.token_hex(32)
    authorization = f"Bearer {pairing_token}".encode()
    cached = None
    cached_at = 0.0

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, _format, *_args):
            pass

        def send_json(self, code, payload):
            data = json.dumps(payload, allow_nan=False).encode()
            self.send_response(code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.send_header("Cache-Control", "no-store")
            self.end_headers()
            self.wfile.write(data)

        def do_GET(self):
            nonlocal cached, cached_at
            if self.path != PATH:
                self.send_json(404, {"error": "not_found"})
                return
            headers = self.headers.get_all("Authorization", [])
            if len(headers) != 1 or not secrets.compare_digest(headers[0].encode(), authorization):
                self.send_json(401, {"error": "invalid_pairing"})
                return
            if cached is None or monotonic() - cached_at >= SNAPSHOT_TTL:
                try:
                    cached = snapshot(fetch())
                    cached_at = monotonic()
                except provider_error as error:
                    print(error, file=sys.stderr)
                    self.send_json(502, {"error": "garmin_unavailable"})
                    return
                except Exception:
                    print("Garmin returned an invalid activity response.", file=sys.stderr)
                    self.send_json(502, {"error": "garmin_unavailable"})
                    return
            self.send_json(200, cached)
            print("Paired iPhone received the Garmin activity preview.", flush=True)

    class LocalServer(HTTPServer):
        def get_request(self):
            connection, peer = super().get_request()
            try:
                connection.settimeout(HANDSHAKE_TIMEOUT)
                return context.wrap_socket(connection, server_side=True), peer
            except OSError:
                connection.close()
                raise

        def handle_error(self, _request, _address):
            # Errors may contain a request header. The local test does not log requests.
            pass

    with TemporaryDirectory(prefix="ryft-garmin-local-") as temporary:
        context, fingerprint = tls(Path(temporary), address)
        with LocalServer((address, PORT), Handler) as server:
            print("Paste this pairing link into RYFT's Garmin local test. Use it on your own iPhone.", flush=True)
            print(f"https://{address}:{PORT}#{fingerprint}.{pairing_token}", flush=True)
            print("Listening for 30 minutes. Press Ctrl+C to stop.", flush=True)
            deadline = monotonic() + LIFETIME
            while (remaining := deadline - monotonic()) > 0:
                server.timeout = remaining
                server.handle_request()
