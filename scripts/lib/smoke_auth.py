"""Verify a smoke session and pass it to the caller through a private file."""

import base64
import http.client
import json
import os
import re
import stat
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


LOOPBACK = {"localhost", "127.0.0.1", "::1"}
PASSWORD_ERRORS = {
    "email_provider_disabled",
    "email_not_confirmed",
    "invalid_credentials",
    "over_request_rate_limit",
    "user_banned",
}
MAX_JSON_BYTES = 128 * 1024
MAX_BEARER_LENGTH = 16 * 1024


class InvalidSmokeAuth(Exception):
    pass


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def checked_url(value, name):
    try:
        parts = urllib.parse.urlsplit(value)
        host, _ = parts.hostname, parts.port
    except ValueError:
        raise InvalidSmokeAuth(f"{name} must be HTTPS or plain HTTP on loopback") from None
    if (
        not host
        or parts.username is not None
        or parts.password is not None
        or parts.query
        or parts.fragment
        or any(ord(char) <= 32 or ord(char) == 127 for char in value)
        or (parts.scheme != "https" and (parts.scheme != "http" or host not in LOOPBACK))
    ):
        raise InvalidSmokeAuth(f"{name} must be HTTPS or plain HTTP on loopback")
    return value.rstrip("/"), host in LOOPBACK


def object_from(stream):
    data = stream.read(MAX_JSON_BYTES + 1)
    if len(data) > MAX_JSON_BYTES:
        raise ValueError("response too large")
    try:
        value = json.loads(data)
    except RecursionError:
        raise ValueError("invalid JSON nesting") from None
    if not isinstance(value, dict):
        raise ValueError("expected an object")
    return value


def request_json(request, label, known_codes=()):
    opener = urllib.request.build_opener(NoRedirect)
    try:
        with opener.open(request, timeout=20) as response:
            if response.status != 200:
                raise InvalidSmokeAuth(f"{label} returned HTTP {response.status}")
            try:
                return object_from(response)
            except ValueError:
                raise InvalidSmokeAuth(f"{label} returned an invalid result (HTTP {response.status})") from None
    except urllib.error.HTTPError as error:
        code = None
        with error:
            try:
                fields = object_from(error)
                for key in ("error_code", "code"):
                    value = fields.get(key)
                    if isinstance(value, str) and value in known_codes:
                        code = value
                        break
            except (OSError, ValueError, http.client.HTTPException):
                pass
        suffix = f" ({code})" if code else ""
        raise InvalidSmokeAuth(f"{label} returned HTTP {error.code}{suffix}") from None
    except (OSError, ValueError, http.client.HTTPException):
        raise InvalidSmokeAuth(f"{label} could not complete") from None


def valid_bearer(value):
    return isinstance(value, str) and 0 < len(value) <= MAX_BEARER_LENGTH and re.fullmatch(r"[!-~]+", value) is not None


def password_token():
    names = ("SUPABASE_URL", "SUPABASE_ANON_KEY", "SMOKE_EMAIL", "SMOKE_PASSWORD")
    if missing := [name for name in names if not os.environ.get(name)]:
        raise InvalidSmokeAuth(f"set SMOKE_TOKEN or supply password sign-in settings (missing {', '.join(missing)})")
    url, _ = checked_url(os.environ["SUPABASE_URL"], "SUPABASE_URL")
    anon = os.environ["SUPABASE_ANON_KEY"]
    if not valid_bearer(anon):
        raise InvalidSmokeAuth("SUPABASE_ANON_KEY has an invalid format")
    body = json.dumps({"email": os.environ["SMOKE_EMAIL"], "password": os.environ["SMOKE_PASSWORD"]}).encode()
    request = urllib.request.Request(
        url + "/auth/v1/token?grant_type=password", body, {"apikey": anon, "Content-Type": "application/json"}
    )
    result = request_json(request, "password sign-in", PASSWORD_ERRORS)
    if not valid_bearer(result.get("access_token")):
        raise InvalidSmokeAuth("password sign-in returned an invalid access token (HTTP 200)")
    return result["access_token"]


def canonical_user(value, message):
    try:
        user = uuid.UUID(value)
        if user.int:
            return str(user)
    except (ValueError, AttributeError, TypeError):
        pass
    raise InvalidSmokeAuth(message)


def check_supplied_jwt(token, expected):
    # Check the expected subject before even loading /v1/me. The API still
    # authenticates the signature, issuer and audience on that request.
    try:
        match = re.fullmatch(r"[A-Za-z0-9_-]+\.([A-Za-z0-9_-]+)\.[A-Za-z0-9_-]+", token)
        if match is None:
            raise ValueError("invalid access token")
        body = match[1]
        claims = json.loads(base64.urlsafe_b64decode(body + "=" * (-len(body) % 4)))
        subject, expiry = claims["sub"], claims["exp"]
        if type(expiry) is not int:
            raise ValueError("invalid expiration")
    except (ValueError, TypeError, KeyError, RecursionError):
        raise InvalidSmokeAuth("SMOKE_TOKEN must be a well-formed Supabase access JWT") from None
    if expiry <= time.time():
        raise InvalidSmokeAuth("SMOKE_TOKEN has expired")
    if canonical_user(subject, "SMOKE_TOKEN has an invalid subject") != expected:
        raise InvalidSmokeAuth("SMOKE_TOKEN subject differs from SMOKE_USER_ID")


def load_session(path, api, ingest):
    try:
        with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK), "rb") as stream:
            info = os.fstat(stream.fileno())
            if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o600 or info.st_uid != os.geteuid():
                raise ValueError("unsafe session file")
            session = object_from(stream)
        if session.get("api") == api and session.get("ingest") == ingest and valid_bearer(session.get("token")):
            return session
    except (OSError, ValueError):
        pass
    raise InvalidSmokeAuth("verified smoke session file is missing, invalid or for another target")


def verify(mode, api_arg, ingest_arg, output):
    api, api_local = checked_url(api_arg, "API_URL")
    ingest, ingest_local = checked_url(ingest_arg, "INGEST_URL")
    supplied = os.environ.get("SMOKE_TOKEN")
    expected = os.environ.get("SMOKE_USER_ID") or None
    if expected:
        expected = canonical_user(expected, "SMOKE_USER_ID must be the dedicated account UUID")
    if mode == "run" and (path := os.environ.get("SMOKE_AUTH_FILE")):
        session = load_session(path, api, ingest)
        token = session["token"]
        subject = canonical_user(session.get("subject"), "verified smoke session has an invalid subject")
        if expected is not None and subject != expected:
            raise InvalidSmokeAuth("verified smoke session subject differs from SMOKE_USER_ID")
        expected = subject
    elif supplied:
        token = supplied
        if not valid_bearer(token):
            raise InvalidSmokeAuth("SMOKE_TOKEN has an invalid format")
        if not (api_local and ingest_local):
            if expected is None:
                raise InvalidSmokeAuth("set SMOKE_USER_ID to the dedicated account UUID for a non-local SMOKE_TOKEN")
            check_supplied_jwt(token, expected)
    else:
        token = password_token()

    me = request_json(urllib.request.Request(api + "/v1/me", headers={"Authorization": f"Bearer {token}"}), "GET /v1/me")
    subject = canonical_user(me.get("id"), "GET /v1/me did not return an account UUID (HTTP 200)")
    if expected is not None and subject != expected:
        raise InvalidSmokeAuth("GET /v1/me did not identify the expected smoke account (HTTP 200)")

    record = {"api": api, "ingest": ingest, "token": token, "subject": subject}
    if mode == "run":
        if type(me.get("beta_access")) is not bool or not isinstance(me.get("tz"), str):
            raise InvalidSmokeAuth("GET /v1/me did not return smoke account settings (HTTP 200)")
        if me.get("consent") is not None and not isinstance(me["consent"], dict):
            raise InvalidSmokeAuth("GET /v1/me returned invalid consent settings (HTTP 200)")
        record["me"] = {key: me.get(key) for key in ("id", "beta_access", "consent", "tz")}
    try:
        with os.fdopen(os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w") as stream:
            json.dump(record, stream)
    except OSError:
        raise InvalidSmokeAuth("could not write protected smoke session file") from None


if __name__ == "__main__":
    if len(sys.argv) != 5 or sys.argv[1] not in ("prepare", "run"):
        sys.exit("smoke-prod: invalid auth helper invocation")
    try:
        verify(*sys.argv[1:])
    except InvalidSmokeAuth as error:
        sys.exit(f"smoke-prod: {error}")
