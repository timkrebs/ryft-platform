"""Verify a smoke session and pass it to the caller through a private file.

Remote default: a Google ID token of the smoke service account, minted with the operator's gcloud login
(iamcredentials generateIdToken), its claims checked here and GET /v1/me bound to SMOKE_USER_ID.

A Google smoke token is minted and sent only to the canonical production origins (https://api.ryftlab.de and
https://ingest.ryftlab.de, after normalisation). Any other target needs SMOKE_TOKEN that is not a Google token.
Modes: prepare <api> <ingest> <file>, run <api> <ingest> <file>, bootstrap <api> <ingest>, mint <file>.

prepare (the preflight before the migration) also requires the smoke account to hold a consent, and warns
when it is not for the PRIVACY_POLICY_VERSION in deploy/run/config.env (the version the commit being deployed
asks for). The smoke token writes no consent (D286): scripts/sql/consent_smoke_account.sql records it.
"""

import base64
import http.client
import json
import os
import re
import stat
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


LOOPBACK = {"localhost", "127.0.0.1", "::1"}
# Google's error "status" values worth showing; anything else is reported as the HTTP status alone.
GOOGLE_STATUSES = {
    "FAILED_PRECONDITION",
    "INVALID_ARGUMENT",
    "NOT_FOUND",
    "PERMISSION_DENIED",
    "RESOURCE_EXHAUSTED",
    "UNAUTHENTICATED",
}
GOOGLE_ISSUERS = {"accounts.google.com", "https://accounts.google.com"}
DEFAULT_SERVICE_ACCOUNT = "ryft-smoke@ryftlab-prod.iam.gserviceaccount.com"
DEFAULT_AUDIENCE = "ryft-smoke"
CANONICAL_API = "https://api.ryftlab.de"
CANONICAL_INGEST = "https://ingest.ryftlab.de"
IAMCREDENTIALS = "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/"
CONFIG_ENV = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "deploy", "run", "config.env")
MAX_TOKEN_LIFETIME = 3700  # Google's ID tokens last one hour
GCLOUD_TIMEOUT = 60
MAX_JSON_BYTES = 128 * 1024
MAX_BEARER_LENGTH = 16 * 1024


class InvalidSmokeAuth(Exception):
    status = None  # the HTTP status behind the failure, when there was one


def http_failure(message, status):
    failure = InvalidSmokeAuth(message)
    failure.status = status
    return failure


# A new api instance answers 503 smoke_jwks_unavailable for about 10 s while its Google keys load, so a read of
# GET /v1/me retries on 503 only: 5 attempts, these waits between them. Tests replace SLEEP.
RETRY_WAITS = (2, 4, 4, 4)
SLEEP = time.sleep


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


def canonical_origin(value, want):
    """True when value is the origin `want` after normalisation: scheme and host in any case, no port or :443,
    no path (one trailing slash is the origin itself), no credentials, query or fragment."""
    try:
        parts = urllib.parse.urlsplit(value)
        host = urllib.parse.urlsplit(want).hostname
        port = parts.port
    except ValueError:
        return False
    return (
        parts.scheme == "https"
        and parts.hostname == host
        and port in (None, 443)
        and parts.netloc.lower() in (host, host + ":443")
        and parts.path in ("", "/")
        and "?" not in value
        and "#" not in value
    )


def targets(api_arg, ingest_arg):
    """(api, api is loopback, ingest, ingest is loopback, canonical). The canonical origins come back normalised."""
    api, api_local = checked_url(api_arg, "API_URL")
    ingest, ingest_local = checked_url(ingest_arg, "INGEST_URL")
    canonical = canonical_origin(api_arg, CANONICAL_API) and canonical_origin(ingest_arg, CANONICAL_INGEST)
    if canonical:
        api, ingest = CANONICAL_API, CANONICAL_INGEST
    return api, api_local, ingest, ingest_local, canonical


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
                raise http_failure(f"{label} returned HTTP {response.status}", response.status)
            try:
                return object_from(response)
            except ValueError:
                raise InvalidSmokeAuth(f"{label} returned an invalid result (HTTP {response.status})") from None
    except urllib.error.HTTPError as error:
        code = None
        with error:
            try:
                fields = object_from(error)
                nested = fields.get("error")
                for source in (fields, nested if isinstance(nested, dict) else {}):
                    for key in ("error_code", "code", "status"):
                        value = source.get(key)
                        if code is None and isinstance(value, str) and value in known_codes:
                            code = value
            except (OSError, ValueError, http.client.HTTPException):
                pass
        suffix = f" ({code})" if code else ""
        raise http_failure(f"{label} returned HTTP {error.code}{suffix}", error.code) from None
    except (OSError, ValueError, http.client.HTTPException):
        raise InvalidSmokeAuth(f"{label} could not complete") from None


def valid_bearer(value):
    return isinstance(value, str) and 0 < len(value) <= MAX_BEARER_LENGTH and re.fullmatch(r"[!-~]+", value) is not None


def read_config():
    try:
        with open(CONFIG_ENV, "rb") as stream:
            data = stream.read(MAX_JSON_BYTES + 1)
    except FileNotFoundError:
        return {}
    except OSError:
        raise InvalidSmokeAuth("deploy/run/config.env could not be read") from None
    if len(data) > MAX_JSON_BYTES:
        raise InvalidSmokeAuth("deploy/run/config.env is too large")
    try:
        lines = data.decode().splitlines()
    except UnicodeDecodeError:
        raise InvalidSmokeAuth("deploy/run/config.env is not UTF-8") from None
    values = {}
    for number, line in enumerate(lines, 1):
        if not line or line.startswith("#"):
            continue
        match = re.fullmatch(r"""([A-Z][A-Z0-9_]*)=([^\s"']*)""", line)
        if match is None:
            raise InvalidSmokeAuth(f"deploy/run/config.env line {number} is not KEY=value")
        if match[1] in values:
            raise InvalidSmokeAuth(f"deploy/run/config.env sets {match[1]} twice")
        values[match[1]] = match[2]
    return values


def setting(name):
    """The environment's value, else deploy/run/config.env's. A missing value or the placeholder is an error."""
    value = os.environ.get(name) or read_config().get(name)
    if not value:
        raise InvalidSmokeAuth(f"{name} is not set (export it or add it to deploy/run/config.env)")
    if value == "FILL_ME_IN":
        raise InvalidSmokeAuth(f"{name} is still FILL_ME_IN (export it or commit the value to deploy/run/config.env)")
    return value


def canonical_user(value, message):
    try:
        user = uuid.UUID(value)
        if user.int:
            return str(user)
    except (ValueError, AttributeError, TypeError):
        pass
    raise InvalidSmokeAuth(message)


def smoke_sa_id():
    sa_id = setting("SMOKE_SA_ID")
    if re.fullmatch(r"[0-9]{1,32}", sa_id) is None:
        raise InvalidSmokeAuth("SMOKE_SA_ID must be the smoke service account's numeric unique ID")
    return sa_id


def smoke_identity():
    """(account UUID, service account unique ID) the smoke session must belong to."""
    user = canonical_user(setting("SMOKE_USER_ID"), "SMOKE_USER_ID must be the dedicated account UUID")
    return user, smoke_sa_id()


def smoke_target():
    """(service account email, audience); the defaults are production's."""
    email = os.environ.get("SMOKE_SERVICE_ACCOUNT") or DEFAULT_SERVICE_ACCOUNT
    audience = os.environ.get("SMOKE_AUDIENCE") or DEFAULT_AUDIENCE
    if re.fullmatch(r"[a-z][a-z0-9-]{4,28}[a-z0-9]@[a-z][a-z0-9-]{4,28}[a-z0-9]\.iam\.gserviceaccount\.com", email) is None:
        raise InvalidSmokeAuth("SMOKE_SERVICE_ACCOUNT must be a service account email")
    if re.fullmatch(r"[A-Za-z0-9._:/-]{1,128}", audience) is None:
        raise InvalidSmokeAuth("SMOKE_AUDIENCE has an invalid format")
    return email, audience


def jwt_claims(token):
    match = re.fullmatch(r"[A-Za-z0-9_-]+\.([A-Za-z0-9_-]+)\.[A-Za-z0-9_-]+", token)
    if match is None:
        raise ValueError("invalid access token")
    body = match[1]
    claims = json.loads(base64.urlsafe_b64decode(body + "=" * (-len(body) % 4)))
    if not isinstance(claims, dict):
        raise ValueError("invalid claims")
    return claims


def check_google_claims(token, label, sa_id):
    """Read the token's claims without verifying the signature (the API does) and refuse a token for anything
    but the smoke account before it is sent anywhere."""
    email, audience = smoke_target()
    try:
        claims = jwt_claims(token)
    except (ValueError, TypeError, RecursionError):
        raise InvalidSmokeAuth(f"{label} must be a well-formed access JWT") from None
    if claims.get("iss") not in GOOGLE_ISSUERS:
        raise InvalidSmokeAuth(f"{label} was not issued by Google")
    if claims.get("aud") != audience:
        raise InvalidSmokeAuth(f"{label} has the wrong audience")
    if claims.get("email") != email:
        raise InvalidSmokeAuth(f"{label} is not for the smoke service account")
    if claims.get("email_verified") is not True:
        raise InvalidSmokeAuth(f"{label} has an unverified email")
    if claims.get("sub") != sa_id:
        raise InvalidSmokeAuth(f"{label} has the wrong subject (SMOKE_SA_ID)")
    expiry = claims.get("exp")
    if type(expiry) is not int:
        raise InvalidSmokeAuth(f"{label} has an invalid expiration")
    now = time.time()
    if expiry <= now:
        raise InvalidSmokeAuth(f"{label} has expired")
    if expiry > now + MAX_TOKEN_LIFETIME:
        raise InvalidSmokeAuth(f"{label} lives longer than a Google ID token does")


def gcloud_access_token():
    try:
        done = subprocess.run(
            ["gcloud", "auth", "print-access-token"],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            timeout=GCLOUD_TIMEOUT,
            check=False,
        )
        token = done.stdout.decode().strip() if done.returncode == 0 else ""
    except (OSError, subprocess.SubprocessError, ValueError):
        token = ""
    if not valid_bearer(token):
        raise InvalidSmokeAuth("gcloud auth print-access-token gave no access token (run gcloud auth login once)")
    return token


def mint_google_token():
    email, audience = smoke_target()
    access = gcloud_access_token()
    request = urllib.request.Request(
        IAMCREDENTIALS + email + ":generateIdToken",
        json.dumps({"audience": audience, "includeEmail": True}).encode(),
        {"Authorization": f"Bearer {access}", "Content-Type": "application/json"},
        method="POST",
    )
    try:
        result = request_json(request, "generateIdToken for the smoke account", GOOGLE_STATUSES)
    except InvalidSmokeAuth as error:
        if "HTTP 403" in str(error):
            raise InvalidSmokeAuth(
                f"{error}; the gcloud account needs roles/iam.serviceAccountOpenIdTokenCreator on the smoke account"
            ) from None
        raise
    if not valid_bearer(result.get("token")):
        raise InvalidSmokeAuth("generateIdToken returned an invalid token (HTTP 200)")
    return result["token"]


def is_google_token(token):
    """True for a JWT whose (unverified) issuer is Google's: the kind the API maps to the smoke account."""
    try:
        return jwt_claims(token).get("iss") in GOOGLE_ISSUERS
    except (ValueError, TypeError, RecursionError):
        return False


NOT_CANONICAL = (
    "a Google smoke token is only used against " + CANONICAL_API + " and " + CANONICAL_INGEST
    + " (for another target set SMOKE_TOKEN to a Supabase access JWT of SMOKE_USER_ID)"
)


def check_supplied_jwt(token, expected):
    # Check the expected subject before even loading /v1/me. The API still
    # authenticates the signature, issuer and audience on that request.
    try:
        claims = jwt_claims(token)
        if claims.get("iss") in GOOGLE_ISSUERS:
            return check_google_claims(token, "SMOKE_TOKEN", smoke_sa_id())
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


def me_request(api, token):
    return urllib.request.Request(api + "/v1/me", headers={"Authorization": f"Bearer {token}"})


def read_me(api, token):
    """GET /v1/me as JSON; a 503 (an instance still loading its keys) is retried, nothing else is."""
    for wait in (*RETRY_WAITS, None):
        try:
            return request_json(me_request(api, token), "GET /v1/me")
        except InvalidSmokeAuth as error:
            if wait is None or error.status != 503:
                raise
        SLEEP(wait)


def me_status(api, token):
    """GET /v1/me's HTTP status, with the same 503 retry; the status after the last attempt is returned."""
    for wait in (*RETRY_WAITS, None):
        status = status_of(me_request(api, token), "GET /v1/me")
        if status != 503 or wait is None:
            return status
        SLEEP(wait)


def policy_version():
    """The privacy policy version the commit being deployed asks for (PRIVACY_POLICY_VERSION: the environment's
    value, else deploy/run/config.env's)."""
    value = setting("PRIVACY_POLICY_VERSION")
    if re.fullmatch(r"[0-9A-Za-z._-]{1,32}", value) is None:
        raise InvalidSmokeAuth("PRIVACY_POLICY_VERSION has an invalid format")
    return value


def require_consent(me):
    """The smoke token writes no consent (D286): the operator records it for the bound account with
    scripts/sql/consent_smoke_account.sql. Fail before the rollout when there is none (the sync's consent gate
    needs an unrevoked one). A consent for another version only warns: the gate accepts any unrevoked consent,
    and /v1/me shows the newest one, so an exact match would block every deploy after a consent recorded ahead
    of a version bump, or for a bump that was reverted."""
    policy = policy_version()
    consent = me.get("consent")
    got = consent.get("policy_version") if isinstance(consent, dict) else None
    if not isinstance(got, str) or re.fullmatch(r"[0-9A-Za-z._-]{1,32}", got) is None:
        raise InvalidSmokeAuth(
            f"the smoke account has no consent for {policy}: record it with scripts/sql/consent_smoke_account.sql"
            " (docs/OPERATIONS.md)"
        )
    if got != policy:
        print(
            f"! the smoke account's consent is for {got}, the policy in force is {policy}:"
            f" record {policy} with scripts/sql/consent_smoke_account.sql (docs/OPERATIONS.md)",
            file=sys.stderr,
        )


def verify(mode, api_arg, ingest_arg, output):
    api, api_local, ingest, ingest_local, canonical = targets(api_arg, ingest_arg)
    supplied = os.environ.get("SMOKE_TOKEN")
    expected = os.environ.get("SMOKE_USER_ID") or None
    if expected:
        expected = canonical_user(expected, "SMOKE_USER_ID must be the dedicated account UUID")
    if mode == "run" and (path := os.environ.get("SMOKE_AUTH_FILE")):
        session = load_session(path, api, ingest)
        token = session["token"]
        if is_google_token(token):
            if not canonical:
                raise InvalidSmokeAuth("verified smoke session: " + NOT_CANONICAL)
            check_google_claims(token, "verified smoke session token", smoke_sa_id())
        subject = canonical_user(session.get("subject"), "verified smoke session has an invalid subject")
        if expected is not None and subject != expected:
            raise InvalidSmokeAuth("verified smoke session subject differs from SMOKE_USER_ID")
        expected = subject
    elif supplied:
        token = supplied
        if not valid_bearer(token):
            raise InvalidSmokeAuth("SMOKE_TOKEN has an invalid format")
        if not canonical and is_google_token(token):
            raise InvalidSmokeAuth("SMOKE_TOKEN: " + NOT_CANONICAL)
        if not (api_local and ingest_local):
            if expected is None:
                expected = smoke_identity()[0]
            check_supplied_jwt(token, expected)
    elif api_local or ingest_local:
        raise InvalidSmokeAuth("set SMOKE_TOKEN for a loopback target (the Google sign-in is for the remote API)")
    elif not canonical:
        raise InvalidSmokeAuth("no smoke token is minted for this target: " + NOT_CANONICAL)
    else:
        expected, sa_id = smoke_identity()
        token = mint_google_token()
        check_google_claims(token, "minted token", sa_id)

    me = read_me(api, token)
    subject = canonical_user(me.get("id"), "GET /v1/me did not return an account UUID (HTTP 200)")
    if expected is not None and subject != expected:
        raise InvalidSmokeAuth("GET /v1/me did not identify the expected smoke account (HTTP 200)")
    if mode == "prepare":
        require_consent(me)

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


def mint(output):
    """Mint the smoke token as the default path does, check its claims, write only the token to a new 0600 file."""
    _, sa_id = smoke_identity()
    token = mint_google_token()
    check_google_claims(token, "minted token", sa_id)
    try:
        with os.fdopen(os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w") as stream:
            stream.write(token)
    except OSError:
        raise InvalidSmokeAuth("could not write protected token file (it must not exist yet)") from None


def status_of(request, label):
    """The HTTP status of a request, whatever it is. No body is read; a redirect is not followed."""
    opener = urllib.request.build_opener(NoRedirect)
    try:
        with opener.open(request, timeout=20) as response:
            return response.status
    except urllib.error.HTTPError as error:
        error.close()
        return error.code
    except (OSError, ValueError, http.client.HTTPException):
        raise InvalidSmokeAuth(f"{label} could not complete") from None


def bootstrap(api_arg, ingest_arg):
    """The first deploy of D286: mint the token (the IAM binding works), then expect the running api to refuse it
    with 401 (the revision predates D286). Writes no session file."""
    api, _, _, _, canonical = targets(api_arg, ingest_arg)
    if not canonical:
        raise InvalidSmokeAuth("no smoke token is minted for this target: " + NOT_CANONICAL)
    sa_id = smoke_sa_id()
    token = mint_google_token()
    check_google_claims(token, "minted token", sa_id)
    status = me_status(api, token)
    if status == 200:
        raise InvalidSmokeAuth(
            "the running api accepts the smoke token: the bootstrap is over (the deploy runs the preflight)"
        )
    if status != 401:
        raise InvalidSmokeAuth(f"GET /v1/me returned HTTP {status}, the bootstrap expects 401 from a revision before D286")
    print("✓ smoke-prod bootstrap: the running revision predates D286; the token mints")


if __name__ == "__main__":
    args = sys.argv[1:]
    if len(args) == 2 and args[0] == "mint":
        action = mint
    elif len(args) == 3 and args[0] == "bootstrap":
        action = bootstrap
    elif len(args) == 4 and args[0] in ("prepare", "run"):
        action = verify
    else:
        sys.exit("smoke-prod: invalid auth helper invocation")
    try:
        action(*(args if action is verify else args[1:]))
    except InvalidSmokeAuth as error:
        sys.exit(f"smoke-prod: {error}")
