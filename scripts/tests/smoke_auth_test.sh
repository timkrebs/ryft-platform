#!/usr/bin/env bash
# shellcheck disable=SC1091,SC2154
# Smoke authentication against fake HTTP responses and a fake gcloud; no listeners, no network.
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
import subprocess
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
api, ingest = "https://api.example.test", "https://ingest.example.test"
owner, other = "20261125-0000-4000-8000-0000000000a1", "20261125-0000-4000-8000-0000000000b2"
sa_id, other_sa_id = "104567890123456789012", "104567890123456789999"
sa_email = "ryft-smoke@ryftlab-prod.iam.gserviceaccount.com"
access = "ya29.fixture-access-marker"
iam_url = "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/" + sa_email + ":generateIdToken"
good_config = f"# comment\n\nSMOKE_USER_ID={owner}\nSMOKE_SA_ID={sa_id}\n"


def jwt(claims):
    body = base64.urlsafe_b64encode(json.dumps(claims).encode()).decode().rstrip("=")
    return "header." + body + ".signature"


def bearer(sub=owner, ttl=300):
    return jwt({"sub": sub, "exp": int(time.time()) + ttl})


def google(ttl=3600, **override):
    claims = {
        "iss": "https://accounts.google.com",
        "aud": "ryft-smoke",
        "email": sa_email,
        "email_verified": True,
        "sub": sa_id,
        "exp": int(time.time()) + ttl,
    }
    claims.update(override)
    return jwt({key: value for key, value in claims.items() if value is not None})


def me(user=owner):
    return {"id": user, "tz": "UTC", "beta_access": True, "consent": None}


def google_error(status, body):
    return urllib.error.HTTPError(iam_url, status, "synthetic", {}, Response(body))


class Response(io.BytesIO):
    status = 200

    def __init__(self, value):
        super().__init__(value if isinstance(value, bytes) else json.dumps(value).encode())


class SmokeAuth(unittest.TestCase):
    def setUp(self):
        tmp = self.enterContext(tempfile.TemporaryDirectory())
        self.tmp = Path(tmp)
        self.session = Path(tmp, "smoke.json")
        self.result = Path(tmp, "verified.json")
        self.config = Path(tmp, "config.env")
        self.config.write_text(good_config)
        self.enterContext(patch.dict(verify.__globals__, {"CONFIG_ENV": str(self.config)}))
        self.client = Mock()
        factory = self.enterContext(patch.object(urllib.request, "build_opener", return_value=self.client))
        self.addCleanup(self.check_no_redirects, factory)
        self.gcloud = self.enterContext(patch.object(
            subprocess, "run", return_value=subprocess.CompletedProcess([], 0, stdout=(access + "\n").encode()),
        ))

    def check_no_redirects(self, factory):
        for call in factory.call_args_list:
            handler, = call.args
            self.assertIsNone(handler().redirect_request(None, None, 307, None, {}, "https://other.example.test"))

    def prepare(self, env=None):
        with patch.dict(os.environ, env or {}, clear=True):
            verify("prepare", api, ingest, self.session)

    def rejected(self, regex, env=None, *forbidden):
        with self.assertRaisesRegex(invalid, regex) as raised:
            self.prepare(env)
        for value in forbidden:
            self.assertNotIn(value, str(raised.exception))
        self.assertFalse(self.session.exists())
        return str(raised.exception)

    def cli(self, env, error):
        self.client.open.side_effect = error
        argv = [helper, "prepare", api, ingest, str(self.session)]
        with patch.dict(os.environ, env, clear=True), patch.object(sys, "argv", argv), self.assertRaises(SystemExit) as fail:
            runpy.run_path(helper, run_name="__main__")
        message = str(fail.exception)
        for value in (access, "sensitive response body", "Traceback"):
            self.assertNotIn(value, message)
        self.assertFalse(self.session.exists())
        return message

    # ---- the default path: gcloud, generateIdToken, claims, /v1/me ----------------------------

    def test_mint_request_shape(self):
        token = google()
        self.client.open.side_effect = [Response({"token": token}), Response(me())]
        self.prepare()
        argv, = self.gcloud.call_args.args
        self.assertEqual(argv, ["gcloud", "auth", "print-access-token"])
        options = self.gcloud.call_args.kwargs
        self.assertFalse(options.get("shell"))
        self.assertEqual(options["stdout"], subprocess.PIPE)
        self.assertEqual(options["stderr"], subprocess.DEVNULL)
        self.assertGreater(options["timeout"], 0)
        mint, read = (call.args[0] for call in self.client.open.call_args_list)
        self.assertEqual(mint.full_url, iam_url)
        self.assertEqual(mint.get_method(), "POST")
        self.assertEqual(json.loads(mint.data), {"audience": "ryft-smoke", "includeEmail": True})
        self.assertEqual(mint.headers["Authorization"], "Bearer " + access)
        self.assertEqual(mint.headers["Content-type"], "application/json")
        self.assertEqual(read.full_url, api + "/v1/me")
        self.assertEqual(read.headers["Authorization"], "Bearer " + token)
        self.assertEqual(stat.S_IMODE(self.session.stat().st_mode), 0o600)
        self.assertEqual(json.loads(self.session.read_bytes())["token"], token)
        self.assertEqual(json.loads(self.session.read_bytes())["subject"], owner)

    def test_environment_overrides_config_and_defaults(self):
        email, audience = "ryft-smoke-test@example-proj.iam.gserviceaccount.com", "other-audience"
        token = google(aud=audience, email=email, sub=other_sa_id)
        self.client.open.side_effect = [Response({"token": token}), Response(me(other))]
        self.prepare({
            "SMOKE_USER_ID": other, "SMOKE_SA_ID": other_sa_id, "SMOKE_SERVICE_ACCOUNT": email, "SMOKE_AUDIENCE": audience,
        })
        mint = self.client.open.call_args_list[0].args[0]
        self.assertTrue(mint.full_url.endswith("/serviceAccounts/" + email + ":generateIdToken"))
        self.assertEqual(json.loads(mint.data)["audience"], audience)

    def test_both_google_issuers_are_accepted(self):
        for issuer in ("accounts.google.com", "https://accounts.google.com"):
            with self.subTest(issuer=issuer):
                self.client.open.side_effect = [Response({"token": google(iss=issuer)}), Response(me())]
                self.prepare()
                self.session.unlink()

    def test_gcloud_failures_are_sanitized(self):
        for name, outcome in (
            ("exit 1", subprocess.CompletedProcess([], 1, stdout=(access + "\n").encode())),
            ("empty", subprocess.CompletedProcess([], 0, stdout=b"\n")),
            ("not UTF-8", subprocess.CompletedProcess([], 0, stdout=b"\xff\xfe")),
            ("whitespace in it", subprocess.CompletedProcess([], 0, stdout=b"two words")),
            ("missing", FileNotFoundError(access)),
            ("timeout", subprocess.TimeoutExpired(["gcloud", access], 60)),
        ):
            with self.subTest(name):
                self.gcloud.side_effect = [outcome]
                message = self.rejected(r"gcloud auth print-access-token gave no access token \(run gcloud auth login once\)",
                                        None, access, "two words")
                self.assertNotIn("Traceback", message)
        self.client.open.assert_not_called()

    def test_generate_id_token_errors_are_sanitized(self):
        denied = {"error": {"code": 403, "message": "sensitive response body " + access, "status": "PERMISSION_DENIED"}}
        cases = (
            (403, denied, r"HTTP 403 \(PERMISSION_DENIED\); the gcloud account needs roles/iam\.serviceAccountOpenIdTokenCreator"),
            (401, {"error": {"status": "UNAUTHENTICATED", "message": access}}, r"HTTP 401 \(UNAUTHENTICATED\)$"),
            (400, {"error": {"status": "SENSITIVE_" + access}}, r"HTTP 400$"),
            (400, {"error": {"status": ["PERMISSION_DENIED"]}}, r"HTTP 400$"),
            (500, b"sensitive response body " + access.encode(), r"HTTP 500$"),
            (500, b"[" * 1200 + b"]" * 1200, r"HTTP 500$"),
        )
        for status, body, regex in cases:
            with self.subTest(status=status, body=type(body)):
                self.client.open.side_effect = google_error(status, body)
                self.rejected("generateIdToken for the smoke account returned " + regex, None, access, "sensitive response body")
        self.assertEqual(self.client.open.call_count, len(cases))  # never a /v1/me after a failed mint

    def test_generate_id_token_bad_success_bodies_are_rejected(self):
        for body, regex in (
            (b"sensitive response body", r"smoke account returned an invalid result \(HTTP 200\)"),
            (b"[1]", r"smoke account returned an invalid result \(HTTP 200\)"),
            ({"token": ""}, r"generateIdToken returned an invalid token \(HTTP 200\)"),
            ({"token": 7}, r"generateIdToken returned an invalid token \(HTTP 200\)"),
            ({"token": "two words"}, r"generateIdToken returned an invalid token \(HTTP 200\)"),
            ({}, r"generateIdToken returned an invalid token \(HTTP 200\)"),
        ):
            with self.subTest(body=body):
                self.client.open.side_effect = [Response(body)]
                self.rejected(regex, None, "sensitive response body", "two words")
        self.client.open.side_effect = OSError("sensitive response body " + access)
        self.rejected("generateIdToken for the smoke account could not complete", None, access, "sensitive response body")

    def test_each_wrong_claim_is_rejected_before_the_api_is_called(self):
        soon = int(time.time())
        for name, claims, regex in (
            ("issuer", {"iss": "https://evil.example.test"}, "not issued by Google"),
            ("no issuer", {"iss": None}, "not issued by Google"),
            ("supabase issuer", {"iss": "https://ref.supabase.co/auth/v1"}, "not issued by Google"),
            ("audience", {"aud": "other"}, "wrong audience"),
            ("audience list", {"aud": ["ryft-smoke"]}, "wrong audience"),
            ("no audience", {"aud": None}, "wrong audience"),
            ("email", {"email": "ryft-deployer@ryftlab-prod.iam.gserviceaccount.com"}, "not for the smoke service account"),
            ("no email", {"email": None}, "not for the smoke service account"),
            ("unverified email", {"email_verified": False}, "unverified email"),
            ("email_verified string", {"email_verified": "true"}, "unverified email"),
            ("no email_verified", {"email_verified": None}, "unverified email"),
            ("subject", {"sub": other_sa_id}, "wrong subject"),
            ("numeric subject", {"sub": int(sa_id)}, "wrong subject"),
            ("no subject", {"sub": None}, "wrong subject"),
            ("expired", {"exp": soon - 1}, "has expired"),
            ("expires now", {"exp": soon}, "has expired"),
            ("lives too long", {"exp": soon + 4000}, "lives longer"),
            ("string exp", {"exp": str(soon + 3600)}, "invalid expiration"),
            ("float exp", {"exp": soon + 3600.5}, "invalid expiration"),
            ("bool exp", {"exp": True}, "invalid expiration"),
            ("no exp", {"exp": None}, "invalid expiration"),
        ):
            with self.subTest(name):
                token = google(**claims)
                self.client.open.reset_mock()
                self.client.open.side_effect = [Response({"token": token}), Response(me())]
                self.rejected(regex, None, token, access)
                self.assertEqual(self.client.open.call_count, 1, "only the mint, never /v1/me")

    def test_minted_token_that_is_not_a_jwt_is_rejected(self):
        for token in ("not-a-jwt", "a.b.c", jwt([1, 2]).replace("header", "x"), "a." + "!" * 5 + ".c"):
            with self.subTest(token=token):
                self.client.open.reset_mock()
                self.client.open.side_effect = [Response({"token": token})]
                self.rejected("minted token must be a well-formed access JWT", None, token)
                self.assertEqual(self.client.open.call_count, 1)

    def test_me_for_another_user_is_rejected(self):
        token = google()
        self.client.open.side_effect = [Response({"token": token}), Response(me(other)), Response({"token": token}), Response({"tz": "UTC"})]
        self.rejected("did not identify the expected smoke account", None, token)
        self.rejected("did not return an account UUID")
        self.assertEqual(self.client.open.call_count, 4)

    def test_me_http_failure_is_sanitized(self):
        token = google()
        self.client.open.side_effect = [Response({"token": token}), urllib.error.HTTPError(api, 401, "x", {}, Response(b"sensitive response body"))]
        self.rejected(r"GET /v1/me returned HTTP 401$", None, token, "sensitive response body")

    def test_preflight_and_post_rollout_share_session(self):
        token = google()
        self.client.open.side_effect = [Response({"token": token}), Response(me()), Response(me())]
        self.prepare()
        self.assertEqual(stat.S_IMODE(self.session.stat().st_mode), 0o600)
        self.assertNotIn("me", json.loads(self.session.read_bytes()))
        with patch.dict(os.environ, {"SMOKE_AUTH_FILE": str(self.session)}, clear=True):
            verify("run", api, ingest, self.result)
        self.assertEqual(json.loads(self.result.read_bytes())["me"], me())
        self.assertEqual(json.loads(self.result.read_bytes())["token"], token)
        self.assertEqual(self.client.open.call_count, 3)  # one mint, two reads
        self.assertEqual(self.gcloud.call_count, 1)

    def test_run_without_a_session_file_mints_its_own(self):
        token = google()
        self.client.open.side_effect = [Response({"token": token}), Response(me())]
        with patch.dict(os.environ, {}, clear=True):
            verify("run", api, ingest, self.result)
        self.assertEqual(json.loads(self.result.read_bytes())["me"], me())
        self.assertEqual(self.gcloud.call_count, 1)

    def test_cached_session_failure_cannot_fall_back_to_minting(self):
        env = {"SMOKE_AUTH_FILE": str(self.session)}
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
        self.gcloud.assert_not_called()

    def test_loopback_target_needs_an_explicit_token(self):
        with patch.dict(os.environ, {}, clear=True), self.assertRaisesRegex(invalid, "loopback target"):
            verify("prepare", "http://localhost:8082", ingest, self.session)
        self.client.open.assert_not_called()
        self.gcloud.assert_not_called()

    def test_failures_through_the_command_line_are_one_clean_line(self):
        env = {"SMOKE_USER_ID": owner, "SMOKE_SA_ID": sa_id}
        message = self.cli(env, google_error(403, {"error": {"status": "PERMISSION_DENIED", "message": "sensitive response body"}}))
        self.assertTrue(message.startswith("smoke-prod: generateIdToken for the smoke account returned HTTP 403 (PERMISSION_DENIED)"))
        self.gcloud.side_effect = FileNotFoundError("gcloud")
        self.assertEqual(self.cli(env, []), "smoke-prod: gcloud auth print-access-token gave no access token (run gcloud auth login once)")

    # ---- SMOKE_USER_ID / SMOKE_SA_ID: environment, else deploy/run/config.env ------------------

    def test_config_env_is_read_strictly(self):
        for name, text, regex in (
            ("user placeholder", f"SMOKE_USER_ID=FILL_ME_IN\nSMOKE_SA_ID={sa_id}\n", r"SMOKE_USER_ID is still FILL_ME_IN"),
            ("sa placeholder", f"SMOKE_USER_ID={owner}\nSMOKE_SA_ID=FILL_ME_IN\n", r"SMOKE_SA_ID is still FILL_ME_IN"),
            ("user missing", f"SMOKE_SA_ID={sa_id}\n", r"SMOKE_USER_ID is not set"),
            ("sa missing", f"SMOKE_USER_ID={owner}\n", r"SMOKE_SA_ID is not set"),
            ("empty value", f"SMOKE_USER_ID=\nSMOKE_SA_ID={sa_id}\n", r"SMOKE_USER_ID is not set"),
            ("spaces around =", f"SMOKE_USER_ID = {owner}\nSMOKE_SA_ID={sa_id}\n", r"line 1 is not KEY=value"),
            ("quoted", f"SMOKE_USER_ID=\"{owner}\"\nSMOKE_SA_ID={sa_id}\n", r"line 1 is not KEY=value"),
            ("inline comment", f"SMOKE_USER_ID={owner} # me\nSMOKE_SA_ID={sa_id}\n", r"line 1 is not KEY=value"),
            ("no equals", f"\nSMOKE_USER_ID\nSMOKE_SA_ID={sa_id}\n", r"line 2 is not KEY=value"),
            ("lower-case key", f"smoke_user_id={owner}\nSMOKE_SA_ID={sa_id}\n", r"line 1 is not KEY=value"),
            ("duplicate", good_config + f"SMOKE_SA_ID={other_sa_id}\n", r"sets SMOKE_SA_ID twice"),
            ("user not a UUID", f"SMOKE_USER_ID=nobody\nSMOKE_SA_ID={sa_id}\n", r"SMOKE_USER_ID must be the dedicated account UUID"),
            ("sa not digits", f"SMOKE_USER_ID={owner}\nSMOKE_SA_ID=abc\n", r"SMOKE_SA_ID must be .* numeric unique ID"),
            ("not UTF-8", None, r"not UTF-8"),
        ):
            with self.subTest(name):
                self.config.write_bytes(b"\xff\xfe" if text is None else text.encode())
                message = self.rejected(regex)
                self.assertNotIn("nobody", message)
        self.config.unlink()
        self.rejected("SMOKE_USER_ID is not set")
        self.client.open.assert_not_called()
        self.gcloud.assert_not_called()  # a bad config stops before gcloud is asked for anything

    def test_environment_wins_over_a_placeholder_in_config_env(self):
        self.config.write_text("SMOKE_USER_ID=FILL_ME_IN\nSMOKE_SA_ID=FILL_ME_IN\n")
        self.client.open.side_effect = [Response({"token": google()}), Response(me())]
        self.prepare({"SMOKE_USER_ID": owner, "SMOKE_SA_ID": sa_id})
        self.session.unlink()
        self.rejected("SMOKE_SA_ID is still FILL_ME_IN", {"SMOKE_USER_ID": owner, "SMOKE_SA_ID": "FILL_ME_IN"})

    def test_the_committed_config_env_parses(self):
        # The real file: either the placeholders or real values, never a malformed line.
        real = Path(helper).resolve().parents[2] / "deploy" / "run" / "config.env"
        with patch.dict(verify.__globals__, {"CONFIG_ENV": str(real)}):
            values = verify.__globals__["read_config"]()
        self.assertIn("SMOKE_USER_ID", values)
        self.assertIn("SMOKE_SA_ID", values)

    def test_bad_target_settings_are_rejected(self):
        for env in ({"SMOKE_SERVICE_ACCOUNT": "x@evil.example.test"}, {"SMOKE_SERVICE_ACCOUNT": "a/../b@p.iam.gserviceaccount.com"},
                    {"SMOKE_AUDIENCE": "two words"}, {"SMOKE_AUDIENCE": "x" * 200}):
            with self.subTest(env=env):
                with self.assertRaisesRegex(invalid, "SMOKE_(SERVICE_ACCOUNT|AUDIENCE) "):
                    self.prepare(env)
        self.client.open.assert_not_called()

    # ---- SMOKE_TOKEN ----------------------------------------------------------------------------

    def test_remote_bearer_is_bound_before_network(self):
        missing = self.tmp / "missing.env"
        for token, fixture, config, error in (
            (bearer(), "", missing, "SMOKE_USER_ID is not set"),
            (bearer(), "", None, None),  # the config's SMOKE_USER_ID stands in for an empty one: passes the binding
            (bearer(), "invalid", None, "must be the dedicated account UUID"),
            (bearer(other), owner, None, "subject differs"),
            (bearer(ttl=-1), owner, None, "expired"),
            ("not-an-access-token", owner, None, "well-formed"),
            ("token\r\nInjected:header", owner, None, "invalid format"),
        ):
            with self.subTest(error=error, fixture=fixture), patch.dict(os.environ, {"SMOKE_TOKEN": token, "SMOKE_USER_ID": fixture}, clear=True):
                with patch.dict(verify.__globals__, {"CONFIG_ENV": str(config or self.config)}):
                    if error is None:
                        self.client.open.return_value = Response(me())
                        verify("prepare", api, ingest, self.session)
                        self.session.unlink()
                        self.client.open.reset_mock()
                    else:
                        with self.assertRaisesRegex(invalid, error):
                            verify("prepare", api, ingest, self.session)
        self.client.open.assert_not_called()
        self.gcloud.assert_not_called()

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
        self.gcloud.assert_not_called()

    def test_remote_google_token_by_hand_is_checked_then_bound(self):
        token = google()
        self.client.open.return_value = Response(me())
        self.prepare({"SMOKE_TOKEN": token})
        req, = (call.args[0] for call in self.client.open.call_args_list)
        self.assertEqual((req.full_url, req.headers["Authorization"]), (api + "/v1/me", "Bearer " + token))
        self.gcloud.assert_not_called()
        self.client.open.reset_mock()
        self.session.unlink()
        for name, claims, regex in (
            ("audience", {"aud": "other"}, "SMOKE_TOKEN has the wrong audience"),
            ("email", {"email": "someone@example.test"}, "SMOKE_TOKEN is not for the smoke service account"),
            ("unverified", {"email_verified": False}, "SMOKE_TOKEN has an unverified email"),
            ("subject", {"sub": other_sa_id}, "SMOKE_TOKEN has the wrong subject"),
            ("expired", {"exp": int(time.time()) - 5}, "SMOKE_TOKEN has expired"),
            ("too long", {"exp": int(time.time()) + 4000}, "SMOKE_TOKEN lives longer"),
        ):
            with self.subTest(name):
                self.rejected(regex, {"SMOKE_TOKEN": google(**claims)})
        self.config.write_text(f"SMOKE_USER_ID={owner}\nSMOKE_SA_ID=FILL_ME_IN\n")
        self.rejected("SMOKE_SA_ID is still FILL_ME_IN", {"SMOKE_TOKEN": token})
        self.client.open.assert_not_called()

    def test_local_api_key_fixture_is_preserved(self):
        self.client.open.return_value = Response(me())
        with patch.dict(os.environ, {"SMOKE_TOKEN": "dev-key:" + owner}, clear=True):
            verify("run", "http://localhost:8082", "http://127.0.0.1:8081", self.result)
        self.assertEqual(json.loads(self.result.read_bytes())["subject"], owner)
        self.gcloud.assert_not_called()

    # ---- `mint <file>`: the token by hand ---------------------------------------------------------

    def run_cli(self, *args, env=None):
        out, err = io.StringIO(), io.StringIO()
        env = {"SMOKE_USER_ID": owner, "SMOKE_SA_ID": sa_id} if env is None else env
        with patch.dict(os.environ, env, clear=True), patch.object(sys, "argv", [helper, *args]), \
                patch.object(sys, "stdout", out), patch.object(sys, "stderr", err):
            try:
                runpy.run_path(helper, run_name="__main__")
                code = None
            except SystemExit as stop:
                code = str(stop)
        self.assertEqual(out.getvalue() + err.getvalue(), "")  # nothing is printed, not even the message
        return code

    def test_mint_writes_only_the_token_to_a_private_new_file(self):
        token = google()
        target = self.tmp / "token"
        self.client.open.side_effect = [Response({"token": token})]
        self.assertIsNone(self.run_cli("mint", str(target)))
        self.assertEqual(target.read_bytes(), token.encode())
        self.assertEqual(stat.S_IMODE(target.stat().st_mode), 0o600)
        self.assertEqual(self.client.open.call_count, 1)  # the mint only, no /v1/me
        mint_request = self.client.open.call_args.args[0]
        self.assertEqual(mint_request.full_url, iam_url)
        self.assertEqual(json.loads(mint_request.data), {"audience": "ryft-smoke", "includeEmail": True})
        self.assertEqual(mint_request.headers["Authorization"], "Bearer " + access)
        self.assertEqual(self.gcloud.call_args.args[0], ["gcloud", "auth", "print-access-token"])

    def test_mint_refuses_an_existing_file_and_keeps_it(self):
        target = self.tmp / "token"
        target.write_text("keep me")
        self.client.open.side_effect = [Response({"token": google()})]
        self.assertEqual(self.run_cli("mint", str(target)), "smoke-prod: could not write protected token file (it must not exist yet)")
        self.assertEqual(target.read_text(), "keep me")
        link = self.tmp / "link"
        link.symlink_to(self.tmp / "elsewhere")
        self.client.open.side_effect = [Response({"token": google()})]
        self.assertIn("could not write", self.run_cli("mint", str(link)))
        self.assertFalse((self.tmp / "elsewhere").exists())

    def test_mint_writes_nothing_when_a_check_fails(self):
        target = self.tmp / "token"
        for name, token in (("audience", google(aud="other")), ("subject", google(sub=other_sa_id)), ("expired", google(ttl=-5))):
            with self.subTest(name):
                self.client.open.side_effect = [Response({"token": token})]
                message = self.run_cli("mint", str(target))
                self.assertTrue(message.startswith("smoke-prod: minted token has "), message)
                self.assertNotIn(token, message)
                self.assertFalse(target.exists())
        self.client.open.side_effect = google_error(403, {"error": {"status": "PERMISSION_DENIED", "message": access}})
        message = self.run_cli("mint", str(target))
        self.assertIn("HTTP 403 (PERMISSION_DENIED)", message)
        self.assertNotIn(access, message)
        self.gcloud.side_effect = FileNotFoundError("gcloud")
        self.assertIn("gcloud auth print-access-token gave no access token", self.run_cli("mint", str(target)))
        self.assertFalse(target.exists())

    def test_mint_needs_exactly_one_path(self):
        for args in (("mint",), ("mint", "a", "b"), ("mint-it", "a")):
            with self.subTest(args=args):
                self.assertEqual(self.run_cli(*args), "smoke-prod: invalid auth helper invocation")
        self.client.open.assert_not_called()
        self.gcloud.assert_not_called()


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

# The real subprocess call, against a fake gcloud that fails: no token in the message, no curl, no network.
cat > "$tmp/bin/gcloud" <<'SH'
#!/usr/bin/env bash
[ "$*" = 'auth print-access-token' ] || { echo "unexpected gcloud arguments: $*" >&2; exit 92; }
echo 'ya29.fixture-secret-marker'
echo 'ERROR: fixture-stderr-marker' >&2
exit 1
SH
chmod +x "$tmp/bin/gcloud"
if env -i PATH="$tmp/bin:$PATH" SMOKE_AUTH_FILE="$tmp/never.json" \
    SMOKE_USER_ID='20261125-0000-4000-8000-0000000000a1' SMOKE_SA_ID='104567890123456789012' \
    bash "$HERE/scripts/smoke_prod.sh" --auth-preflight > "$tmp/gcloud_failed" 2>&1; then
  echo 'smoke-prod passed with a failing gcloud' >&2
  exit 1
fi
grep -qx 'smoke-prod: gcloud auth print-access-token gave no access token (run gcloud auth login once)' "$tmp/gcloud_failed"
if grep -q 'fixture-secret-marker\|fixture-stderr-marker\|unexpected' "$tmp/gcloud_failed"; then
  echo 'smoke-prod leaked gcloud output' >&2
  exit 1
fi
[ ! -e "$tmp/never.json" ]
