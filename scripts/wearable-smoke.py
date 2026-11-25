#!/usr/bin/env python3
"""Read-only checks against Garmin Personal API or a Suunto development app."""

import argparse
import base64
import getpass
import json
import secrets
import subprocess
import sys
import webbrowser
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
from tempfile import TemporaryDirectory
from time import monotonic
from urllib.error import HTTPError, URLError
from urllib.parse import parse_qs, urlencode, urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener


GARMIN = "https://personal.connectapi.garmin.com"
SUUNTO_OAUTH = "https://cloudapi-oauth.suunto.com"
SUUNTO_API = "https://cloudapi.suunto.com"
CALLBACK_PORT = 8765
CALLBACK_PATH = "/callback"
SUUNTO_REDIRECT = f"http://127.0.0.1:{CALLBACK_PORT}{CALLBACK_PATH}"
MAX_RESPONSE = 4 * 1024 * 1024
REQUEST_TIMEOUT = 20
CALLBACK_TIMEOUT = 180


class SmokeError(Exception):
    pass


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def request(label, url, headers, *, method="GET", body=None):
    req = Request(url, headers={"Accept": "*/*", **headers}, data=body, method=method)
    try:
        with build_opener(NoRedirect()).open(req, timeout=REQUEST_TIMEOUT) as response:
            data = response.read(MAX_RESPONSE + 1)
    except HTTPError as error:
        error.close()
        raise SmokeError(f"{label} returned HTTP {error.code}.") from None
    except (URLError, TimeoutError):
        raise SmokeError(f"Could not reach {label}.") from None
    if len(data) > MAX_RESPONSE:
        raise SmokeError(f"{label} returned more data than this check accepts.")
    return data


def secret(prompt):
    value = getpass.getpass(prompt).strip()
    if not value or "\r" in value or "\n" in value:
        raise SmokeError("A credential is missing or malformed.")
    return value


def token(value):
    if not isinstance(value, str) or not value or not value.isascii() or any(char.isspace() for char in value):
        raise SmokeError("The provider response is missing a usable access token.")
    return value


def garmin_access(key):
    raw_token = request(
        "Garmin token exchange",
        GARMIN + "/personalapitoken-service/personal-api/personal-api-key/token",
        {"Authorization": f"ApiKey {key}"},
        method="POST",
    )
    return token(raw_token.decode("utf-8").strip())


def garmin_activities(access, *, limit=1):
    data = request(
        "Garmin activity list",
        GARMIN + "/activitylist-service/activities/search/activities?" + urlencode({"start": 0, "limit": limit}),
        {"Authorization": f"Bearer {access}"},
    )
    activities = json.loads(data) if data else []
    if not isinstance(activities, list):
        raise SmokeError("Garmin returned an unexpected activity list.")
    return activities[:limit]


def garmin_usb(access, device):
    from garmin_local_api import snapshot

    payload = snapshot(garmin_activities(access, limit=5))
    with TemporaryDirectory(prefix="ryft-garmin-usb-") as temp:
        preview = Path(temp) / "ryft-garmin-preview.json"
        preview.write_text(json.dumps(payload, allow_nan=False), encoding="utf-8")
        try:
            subprocess.run(
                ["xcrun", "devicectl", "--timeout", "20", "device", "copy", "to", "--device", device,
                 "--source", str(preview), "--destination", f"tmp/{preview.name}",
                 "--domain-type", "appDataContainer", "--domain-identifier", "dev.timkrebs.ryft"],
                check=True, timeout=25, stdout=subprocess.DEVNULL,
            )
        except (OSError, subprocess.SubprocessError):
            raise SmokeError("Could not copy the preview. Unlock your connected iPhone and check that the RYFT Debug build is installed.") from None
    print(f"Copied {len(payload['activities'])} Garmin activities. Tap Load USB preview in RYFT.")


def garmin(listen=None, usb_device=None):
    if listen is not None:
        from garmin_local_api import lan_ip, serve

        try:
            listen = lan_ip(listen)
        except ValueError:
            raise SmokeError("Pass your Mac's private Wi-Fi IPv4 address to --serve.") from None
    access = garmin_access(secret("Garmin Personal API key: "))
    print("Garmin accepted the Personal API key.")
    if usb_device is not None:
        garmin_usb(access, usb_device)
        return
    if listen is not None:
        try:
            serve(listen, lambda: garmin_activities(access, limit=5), provider_error=SmokeError)
        except (ValueError, OSError):
            raise SmokeError("Could not start the local API. Check the Wi-Fi address, OpenSSL and port 8785.") from None
        return
    activities = garmin_activities(access)
    print(f"Garmin activity access verified. Activities in the test page: {len(activities)}.")


def suunto_code(client_id):
    state = secrets.token_urlsafe(32)
    result = {}

    class Callback(BaseHTTPRequestHandler):
        def setup(self):
            self.request.settimeout(REQUEST_TIMEOUT)
            super().setup()

        def log_message(self, _format, *_args):
            pass

        def do_GET(self):
            target = urlsplit(self.path)
            fields = parse_qs(target.query, keep_blank_values=True)
            if target.path != CALLBACK_PATH or fields.get("state") != [state]:
                self.send_error(400, "Invalid authorization callback")
                return
            codes = fields.get("code", [])
            if len(codes) == 1 and codes[0] and "error" not in fields:
                result["code"] = codes[0]
            else:
                result["denied"] = True
            message = b"Authorization response received. Return to the terminal."
            self.send_response(200)
            self.send_header("Content-Type", "text/plain; charset=utf-8")
            self.send_header("Content-Length", str(len(message)))
            self.send_header("Cache-Control", "no-store")
            self.send_header("Referrer-Policy", "no-referrer")
            self.end_headers()
            self.wfile.write(message)

    params = urlencode({"response_type": "code", "client_id": client_id, "redirect_uri": SUUNTO_REDIRECT, "state": state})
    authorize = SUUNTO_OAUTH + "/oauth/authorize?" + params
    with HTTPServer(("127.0.0.1", CALLBACK_PORT), Callback) as server:
        print(f"Sign in to Suunto in your browser. The registered redirect must be {SUUNTO_REDIRECT}.")
        if not webbrowser.open(authorize):
            print(authorize)
        deadline = monotonic() + CALLBACK_TIMEOUT
        while not result and (time_left := deadline - monotonic()) > 0:
            server.timeout = time_left
            server.handle_request()
    if "code" not in result:
        raise SmokeError("Suunto authorization was declined or no matching callback arrived.")
    return result["code"]


def suunto():
    client_id = input("Suunto OAuth Client ID: ").strip()
    if not client_id or ":" in client_id:
        raise SmokeError("Enter the Client ID from your Suunto API Zone application.")
    client_secret = secret("Suunto OAuth Client Secret: ")
    subscription = secret("Suunto Developer API Subscription Key: ")
    code = suunto_code(client_id)
    basic = base64.b64encode(f"{client_id}:{client_secret}".encode()).decode()
    form = urlencode({"grant_type": "authorization_code", "redirect_uri": SUUNTO_REDIRECT, "code": code}).encode()
    payload = json.loads(request(
        "Suunto token exchange",
        SUUNTO_OAUTH + "/oauth/token",
        {"Authorization": f"Basic {basic}", "Content-Type": "application/x-www-form-urlencoded"},
        method="POST",
        body=form,
    ))
    if not isinstance(payload, dict) or str(payload.get("token_type", "")).lower() != "bearer":
        raise SmokeError("Suunto returned an unexpected token response.")
    access = token(payload.get("access_token"))
    print("Suunto accepted the OAuth grant.")
    request(
        "Suunto workout list",
        SUUNTO_API + "/v2/workouts",
        {"Authorization": f"Bearer {access}", "Ocp-Apim-Subscription-Key": subscription},
    )
    print("Suunto workout access verified.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("provider", choices=("garmin", "suunto"))
    output = parser.add_mutually_exclusive_group()
    output.add_argument("--serve", metavar="MAC_LAN_IP", help="Garmin only. Start the local iPhone test API.")
    output.add_argument("--usb-device", metavar="DEVICE", help="Garmin only. Copy the preview to a connected iPhone with Xcode.")
    args = parser.parse_args()
    if (args.serve is not None or args.usb_device is not None) and args.provider != "garmin":
        parser.error("--serve and --usb-device are available for Garmin")
    if not sys.stdin.isatty() or not sys.stderr.isatty():
        parser.error("run this check in an interactive terminal")
    try:
        if args.provider == "garmin":
            garmin(args.serve, args.usb_device)
        else:
            suunto()
    except SmokeError as error:
        print(error, file=sys.stderr)
        return 1
    except (ValueError, OSError, EOFError):
        print("Invalid provider response or unable to open the local authorization callback.", file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        return 130
    return 0


if __name__ == "__main__":
    sys.exit(main())
