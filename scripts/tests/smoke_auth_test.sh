#!/usr/bin/env bash
# shellcheck disable=SC1091,SC2154
# Smoke authentication against fake HTTP responses; no listeners or network.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
python3 -I - "$HERE/scripts/lib/smoke_auth.py" <<'PY'
import base64
import io
import json
import os
from pathlib import Path
import runpy
import stat
import sys
import tempfile
import time
import unittest
import urllib.error
import urllib.request
from unittest.mock import Mock, patch

helper = sys.argv[1]
auth = runpy.run_path(helper)
verify = auth["verify"]
invalid = auth["InvalidSmokeAuth"]
api, ingest, supabase = "https://api.example.test", "https://ingest.example.test", "https://auth.example.test"
owner, other = "20261125-0000-4000-8000-0000000000a1", "20261125-0000-4000-8000-0000000000b2"
email, password, key = "fixture@example.test", "fixture-secret-marker", "fixture-public-key"
password_env = {"SUPABASE_URL": supabase, "SUPABASE_ANON_KEY": key, "SMOKE_EMAIL": email, "SMOKE_PASSWORD": password}


def bearer(sub=owner, ttl=300):
    claims = json.dumps({"sub": sub, "exp": int(time.time()) + ttl}).encode()
    return "header." + base64.urlsafe_b64encode(claims).decode().rstrip("=") + ".signature"


def me(user=owner):
    return {"id": user, "tz": "UTC", "beta_access": True, "consent": None}


class Response(io.BytesIO):
    status = 200

    def __init__(self, value):
        super().__init__(value if isinstance(value, bytes) else json.dumps(value).encode())


class SmokeAuth(unittest.TestCase):
    def setUp(self):
        tmp = self.enterContext(tempfile.TemporaryDirectory())
        self.session = Path(tmp, "smoke.json")
        self.result = Path(tmp, "verified.json")
        self.client = Mock()
        factory = self.enterContext(patch.object(urllib.request, "build_opener", return_value=self.client))
        self.addCleanup(self.check_no_redirects, factory)

    def check_no_redirects(self, factory):
        for call in factory.call_args_list:
            handler, = call.args
            self.assertIsNone(handler().redirect_request(None, None, 307, None, {}, "https://other.example.test"))

    def cli(self, env, status, body):
        self.client.open.side_effect = urllib.error.HTTPError(supabase, status, "synthetic", {}, Response(body))
        argv = [helper, "prepare", api, ingest, str(self.session)]
        with patch.dict(os.environ, env, clear=True), patch.object(sys, "argv", argv), self.assertRaises(SystemExit) as fail:
            runpy.run_path(helper, run_name="__main__")
        message = str(fail.exception)
        for value in (email, password, key, "sensitive response body", "Traceback"):
            self.assertNotIn(value, message)
        self.assertFalse(self.session.exists())
        return message

    def test_password_error_is_sanitized(self):
        self.assertIn("HTTP 422 (email_provider_disabled)", self.cli(password_env, 422, {
            "error_code": "email_provider_disabled", "msg": "sensitive response body " + password,
        }))

    def test_non_string_and_unknown_password_errors_are_safe(self):
        for body in (
            b"sensitive response body", {"error_code": []}, {"code": {"secret": password}}, {"code": password},
            b"[" * 1200 + b"]" * 1200,
        ):
            with self.subTest(body=type(body)):
                self.assertEqual(self.cli(password_env, 400, body), "smoke-prod: password sign-in returned HTTP 400")

    def test_password_preflight_and_post_rollout_share_session(self):
        token = bearer()
        self.client.open.side_effect = [Response({"access_token": token}), Response(me()), Response(me())]
        with patch.dict(os.environ, password_env, clear=True):
            verify("prepare", api, ingest, self.session)
        self.assertEqual(stat.S_IMODE(self.session.stat().st_mode), 0o600)
        self.assertNotIn("me", json.loads(self.session.read_bytes()))
        request = self.client.open.call_args_list[0].args[0]
        self.assertEqual(request.full_url, supabase + "/auth/v1/token?grant_type=password")
        self.assertEqual(json.loads(request.data), {"email": email, "password": password})
        with patch.dict(os.environ, {"SMOKE_AUTH_FILE": str(self.session)}, clear=True):
            verify("run", api, ingest, self.result)
        self.assertEqual(json.loads(self.result.read_bytes())["me"], me())
        self.assertEqual(self.client.open.call_count, 3)

    def test_invalid_password_success_body_is_rejected(self):
        self.client.open.return_value = Response(b"sensitive response body")
        with patch.dict(os.environ, password_env, clear=True), self.assertRaisesRegex(invalid, r"invalid result \(HTTP 200\)"):
            verify("prepare", api, ingest, self.session)

    def test_remote_bearer_is_bound_before_network(self):
        for token, fixture, error in (
            (bearer(), "", "set SMOKE_USER_ID"),
            (bearer(), "invalid", "must be the dedicated account UUID"),
            (bearer(other), owner, "subject differs"),
            (bearer(ttl=-1), owner, "expired"),
            ("not-an-access-token", owner, "well-formed"),
            ("token\r\nInjected:header", owner, "invalid format"),
        ):
            with self.subTest(error=error), patch.dict(os.environ, {"SMOKE_TOKEN": token, "SMOKE_USER_ID": fixture}, clear=True):
                with self.assertRaisesRegex(invalid, error):
                    verify("prepare", api, ingest, self.session)
        self.client.open.assert_not_called()

    def test_remote_bearer_requires_the_verified_api_subject(self):
        token = bearer()
        self.client.open.side_effect = [Response(me(other)), Response(me())]
        with patch.dict(os.environ, {"SMOKE_TOKEN": token, "SMOKE_USER_ID": owner}, clear=True):
            with self.assertRaisesRegex(invalid, "did not identify the expected smoke account"):
                verify("prepare", api, ingest, self.session)
            self.assertFalse(self.session.exists())
            verify("prepare", api, ingest, self.session)
        req = self.client.open.call_args.args[0]
        self.assertEqual(req.headers["Authorization"], "Bearer " + token)
        self.assertEqual(req.full_url, api + "/v1/me")

    def test_cached_session_failure_cannot_fall_back_to_password(self):
        env = {**password_env, "SMOKE_AUTH_FILE": str(self.session)}
        with patch.dict(os.environ, env, clear=True), self.assertRaisesRegex(invalid, "session file is missing"):
            verify("run", api, ingest, self.result)
        self.session.write_text(json.dumps({"api": api, "ingest": ingest, "token": bearer(), "subject": owner}))
        self.session.chmod(0o644)
        with patch.dict(os.environ, env, clear=True), self.assertRaisesRegex(invalid, "session file is missing"):
            verify("run", api, ingest, self.result)
        self.session.chmod(0o600)
        with patch.dict(os.environ, env, clear=True), self.assertRaisesRegex(invalid, "another target"):
            verify("run", api, "https://other.example.test", self.result)
        with patch.dict(os.environ, {**env, "SMOKE_USER_ID": other}, clear=True), self.assertRaisesRegex(invalid, "subject differs"):
            verify("run", api, ingest, self.result)
        self.client.open.assert_not_called()

    def test_local_api_key_fixture_is_preserved(self):
        self.client.open.return_value = Response(me())
        with patch.dict(os.environ, {"SMOKE_TOKEN": "dev-key:" + owner}, clear=True):
            verify("run", "http://localhost:8082", "http://127.0.0.1:8081", self.result)
        self.assertEqual(json.loads(self.result.read_bytes())["subject"], owner)


unittest.main(argv=[sys.argv[0]])
PY

mkdir "$tmp/bin"
cat > "$tmp/bin/curl" <<'SH'
#!/usr/bin/env bash
echo 'unexpected curl during rejected authentication' >&2
exit 91
SH
chmod +x "$tmp/bin/curl"
if env -i PATH="$tmp/bin:$PATH" SMOKE_TOKEN='invalid-fixture-token' SMOKE_USER_ID='20261125-0000-4000-8000-0000000000a1' \
    bash "$HERE/scripts/smoke_prod.sh" > "$tmp/rejected" 2>&1; then
  echo 'smoke-prod accepted an invalid remote token' >&2
  exit 1
fi
grep -qx 'smoke-prod: SMOKE_TOKEN must be a well-formed Supabase access JWT' "$tmp/rejected"
