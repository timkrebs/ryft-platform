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
api, ingest = "https://api.ryftlab.de", "https://ingest.ryftlab.de"  # the only origins a Google smoke token goes to
remote_api, remote_ingest = "https://api.example.test", "https://ingest.example.test"
owner, other = "20261125-0000-4000-8000-0000000000a1", "20261125-0000-4000-8000-0000000000b2"
sa_id, other_sa_id = "104567890123456789012", "104567890123456789999"
sa_email = "ryft-smoke@ryftlab-prod.iam.gserviceaccount.com"
access = "ya29.fixture-access-marker"
iam_url = "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/" + sa_email + ":generateIdToken"
policy = "2026-10-26"  # PRIVACY_POLICY_VERSION in the fixture config.env: the version the deploy will ask for
good_config = f"# comment\n\nSMOKE_USER_ID={owner}\nSMOKE_SA_ID={sa_id}\nPRIVACY_POLICY_VERSION={policy}\n"
consent_hint = (
    "the smoke account has no consent for %s: record it with scripts/sql/consent_smoke_account.sql (docs/OPERATIONS.md)"
)


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


def me(user=owner, consent=...):
    if consent is ...:  # the account has its consent for the policy in force, as the operator records it
        consent = {"policy_version": policy, "granted_at": "2026-10-26T09:00:00Z"}
    return {"id": user, "tz": "UTC", "beta_access": True, "consent": consent}


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
        self.sleep = Mock()  # no test waits: the helper's retry pause is this, also in modules run as __main__
        self.enterContext(patch.dict(verify.__globals__, {"CONFIG_ENV": str(self.config), "SLEEP": self.sleep}))
        self.enterContext(patch.object(time, "sleep", self.sleep))
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
        self.config.write_text(f"SMOKE_USER_ID=FILL_ME_IN\nSMOKE_SA_ID=FILL_ME_IN\nPRIVACY_POLICY_VERSION={policy}\n")
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
        self.assertRegex(values["PRIVACY_POLICY_VERSION"], r"^\d{4}-\d{2}-\d{2}$")  # what prepare compares the consent with

    # ---- prepare: the smoke account's consent is for config.env's PRIVACY_POLICY_VERSION (D286) -------
    # The smoke token writes no consent; a missing or stale one fails the preflight, before the migration.

    def test_prepare_passes_when_the_consent_matches_config_env(self):
        self.client.open.side_effect = [Response({"token": google()}), Response(me())]
        self.prepare()
        self.assertTrue(self.session.exists())

    def test_prepare_fails_on_a_missing_or_stale_consent(self):
        for name, consent in (
            ("none", None),
            ("older version", {"policy_version": "2026-01-01", "granted_at": "2026-01-01T00:00:00Z"}),
            ("newer version", {"policy_version": "2026-12-01", "granted_at": "2026-12-01T00:00:00Z"}),
            ("no version", {"granted_at": "2026-10-26T09:00:00Z"}),
            ("numeric version", {"policy_version": 20261026}),
            ("not an object", "2026-10-26"),
        ):
            with self.subTest(name):
                token = google()
                self.client.open.reset_mock()
                self.client.open.side_effect = [Response({"token": token}), Response(me(consent=consent))]
                message = self.rejected(r"\Athe smoke account has no consent for 2026-10-26: record it with "
                                        r"scripts/sql/consent_smoke_account\.sql \(docs/OPERATIONS\.md\)\Z", None, token, access)
                self.assertEqual(message, consent_hint % policy)
                self.assertEqual(self.client.open.call_count, 2)  # the mint and one read: no write of any kind
                self.assertEqual([call.args[0].get_method() for call in self.client.open.call_args_list], ["POST", "GET"])
                self.assertNotIn("/consent", " ".join(call.args[0].full_url for call in self.client.open.call_args_list))

    def test_prepare_compares_with_the_version_in_config_env_not_a_fixed_one(self):
        self.config.write_text(good_config.replace(policy, "2026-12-01"))
        self.client.open.side_effect = [Response({"token": google()}), Response(me())]
        self.assertEqual(self.rejected(r"no consent for 2026-12-01"), consent_hint % "2026-12-01")
        self.client.open.side_effect = [Response({"token": google()}), Response(me(consent={"policy_version": "2026-12-01"}))]
        self.prepare()

    def test_prepare_environment_overrides_the_policy_in_config_env(self):
        self.client.open.side_effect = [Response({"token": google()}), Response(me())]
        self.rejected("no consent for 2026-12-01", {"PRIVACY_POLICY_VERSION": "2026-12-01"})
        self.client.open.side_effect = [Response({"token": google()}), Response(me())]
        self.prepare({"PRIVACY_POLICY_VERSION": policy})

    def test_prepare_needs_a_policy_version_to_compare_with(self):
        for name, text, regex in (
            ("missing", f"SMOKE_USER_ID={owner}\nSMOKE_SA_ID={sa_id}\n", "PRIVACY_POLICY_VERSION is not set"),
            ("placeholder", good_config.replace(policy, "FILL_ME_IN"), "PRIVACY_POLICY_VERSION is still FILL_ME_IN"),
        ):
            with self.subTest(name):
                self.config.write_text(text)
                self.client.open.side_effect = [Response({"token": google()}), Response(me())]
                self.rejected(regex)
        self.client.open.side_effect = [Response({"token": google()}), Response(me())]
        self.rejected("PRIVACY_POLICY_VERSION has an invalid format", {"PRIVACY_POLICY_VERSION": "2026 10 26\nx"})

    def test_prepare_checks_identity_before_consent(self):
        self.client.open.side_effect = [Response({"token": google()}), Response(me(other, consent=None))]
        self.rejected("did not identify the expected smoke account")

    def test_run_does_not_check_consent_the_script_does(self):
        # `run` hands /v1/me to smoke_prod.sh, which fails with the same text; the helper's run mode stays neutral.
        self.client.open.side_effect = [Response({"token": google()}), Response(me(consent=None))]
        with patch.dict(os.environ, {}, clear=True):
            verify("run", api, ingest, self.result)
        self.assertIsNone(json.loads(self.result.read_bytes())["me"]["consent"])

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
                        verify("prepare", remote_api, remote_ingest, self.session)
                        self.session.unlink()
                        self.client.open.reset_mock()
                    else:
                        with self.assertRaisesRegex(invalid, error):
                            verify("prepare", remote_api, remote_ingest, self.session)
        self.client.open.assert_not_called()
        self.gcloud.assert_not_called()

    def test_remote_bearer_requires_the_verified_api_subject(self):
        token = bearer()
        self.client.open.side_effect = [Response(me(other)), Response(me())]
        with patch.dict(os.environ, {"SMOKE_TOKEN": token, "SMOKE_USER_ID": owner}, clear=True):
            with self.assertRaisesRegex(invalid, "did not identify the expected smoke account"):
                verify("prepare", remote_api, remote_ingest, self.session)
            self.assertFalse(self.session.exists())
            verify("prepare", remote_api, remote_ingest, self.session)
        req = self.client.open.call_args.args[0]
        self.assertEqual(req.headers["Authorization"], "Bearer " + token)
        self.assertEqual(req.full_url, remote_api + "/v1/me")
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

    # ---- canonical origins: a Google smoke token goes to api.ryftlab.de / ingest.ryftlab.de only ------

    def no_calls(self):
        self.client.open.assert_not_called()
        self.gcloud.assert_not_called()

    def test_canonical_origins_are_normalised(self):
        for name, given_api, given_ingest in (
            ("host case", "https://API.RyftLab.de", "https://Ingest.ryftlab.DE"),
            ("port 443", "https://api.ryftlab.de:443", "https://ingest.ryftlab.de:443"),
            ("trailing slash", "https://api.ryftlab.de/", "https://ingest.ryftlab.de/"),
            ("all of it", "HTTPS://Api.RyftLab.De:443/", "https://INGEST.ryftlab.de:443/"),
        ):
            with self.subTest(name):
                self.client.open.reset_mock()
                self.client.open.side_effect = [Response({"token": google()}), Response(me())]
                with patch.dict(os.environ, {}, clear=True):
                    verify("run", given_api, given_ingest, self.result)
                self.assertEqual(self.client.open.call_args.args[0].full_url, api + "/v1/me")
                record = json.loads(self.result.read_bytes())
                self.assertEqual((record["api"], record["ingest"]), (api, ingest))  # the shell uses these
                self.result.unlink()

    def test_lookalike_origins_are_refused_before_any_call(self):
        def lookalikes(host):
            return (
                "https://%s.evil.test" % host, "https://evil-%s" % host, "https://%s." % host, "https://%s:8443" % host,
                "https://%s:" % host, "http://%s" % host, "https://%s/v1" % host, "https://%s//" % host,
                "https://%s@evil.test" % host, "https://evil.test@%s" % host, "https://%s?x=1" % host, "https://%s/#x" % host,
            )
        cases = [(bad, ingest) for bad in lookalikes("api.ryftlab.de")]
        cases += [(api, bad) for bad in lookalikes("ingest.ryftlab.de")]
        cases += [(ingest, ingest), (api, api), ("https://ryftlab.de", ingest), (api, "https://ryftlab.de"),
                  ("http://localhost:8082", ingest), (api, "http://127.0.0.1:8081")]
        regex = r"must be HTTPS or plain HTTP on loopback|no smoke token is minted|loopback target"
        for given_api, given_ingest in cases:
            for mode in ("prepare", "run"):
                with self.subTest(api=given_api, ingest=given_ingest, mode=mode):
                    with patch.dict(os.environ, {}, clear=True), self.assertRaisesRegex(invalid, regex):
                        verify(mode, given_api, given_ingest, self.result)
        self.no_calls()
        self.assertFalse(self.result.exists())

    def test_non_canonical_remote_refuses_a_google_smoke_token(self):
        token = google()
        for given_api, given_ingest in ((remote_api, remote_ingest), (api, remote_ingest), (remote_api, ingest),
                                         ("https://api.ryftlab.de.evil.test", ingest),
                                         ("http://localhost:8082", "http://127.0.0.1:8081")):
            with self.subTest(api=given_api, ingest=given_ingest):
                with patch.dict(os.environ, {"SMOKE_TOKEN": token}, clear=True), \
                        self.assertRaisesRegex(invalid, "SMOKE_TOKEN: a Google smoke token is only used against https://api.ryftlab.de") as raised:
                    verify("prepare", given_api, given_ingest, self.session)
                self.assertNotIn(token, str(raised.exception))
        self.no_calls()
        self.assertFalse(self.session.exists())

    def test_non_canonical_remote_does_not_mint(self):
        with patch.dict(os.environ, {}, clear=True), self.assertRaisesRegex(invalid, "no smoke token is minted for this target"):
            verify("prepare", remote_api, remote_ingest, self.session)
        self.no_calls()

    def test_non_canonical_remote_accepts_a_supabase_jwt_of_the_smoke_user(self):
        self.client.open.return_value = Response(me())
        with patch.dict(os.environ, {"SMOKE_TOKEN": bearer(), "SMOKE_USER_ID": owner}, clear=True):
            verify("prepare", remote_api, remote_ingest, self.session)
        self.assertEqual(self.client.open.call_args.args[0].full_url, remote_api + "/v1/me")
        self.gcloud.assert_not_called()

    def test_session_with_a_google_token_for_a_non_canonical_target_is_refused(self):
        for given_api, given_ingest in ((remote_api, remote_ingest), (api, remote_ingest), (remote_api, ingest)):
            with self.subTest(api=given_api, ingest=given_ingest):
                token = google()
                self.session.unlink(missing_ok=True)
                with os.fdopen(os.open(self.session, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w") as stream:
                    json.dump({"api": given_api, "ingest": given_ingest, "token": token, "subject": owner}, stream)
                with patch.dict(os.environ, {"SMOKE_AUTH_FILE": str(self.session)}, clear=True), \
                        self.assertRaisesRegex(invalid, "verified smoke session: a Google smoke token is only used against") as raised:
                    verify("run", given_api, given_ingest, self.result)
                self.assertNotIn(token, str(raised.exception))
        self.no_calls()
        self.assertFalse(self.result.exists())

    def test_session_google_token_is_checked_again_and_a_non_google_one_is_not_blocked(self):
        for name, token, regex in (("expired", google(ttl=-5), "verified smoke session token has expired"),
                                   ("wrong audience", google(aud="other"), "verified smoke session token has the wrong audience")):
            with self.subTest(name):
                self.session.unlink(missing_ok=True)
                with os.fdopen(os.open(self.session, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w") as stream:
                    json.dump({"api": api, "ingest": ingest, "token": token, "subject": owner}, stream)
                with patch.dict(os.environ, {"SMOKE_AUTH_FILE": str(self.session)}, clear=True), self.assertRaisesRegex(invalid, regex):
                    verify("run", api, ingest, self.result)
        self.no_calls()
        self.session.unlink()
        with os.fdopen(os.open(self.session, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w") as stream:
            json.dump({"api": remote_api, "ingest": remote_ingest, "token": bearer(), "subject": owner}, stream)
        self.client.open.return_value = Response(me())
        with patch.dict(os.environ, {"SMOKE_AUTH_FILE": str(self.session)}, clear=True):
            verify("run", remote_api, remote_ingest, self.result)
        self.gcloud.assert_not_called()

    def test_mint_sends_nowhere_but_the_mint_request(self):
        # `mint` has no target: it asks iamcredentials and writes the file. The canonical rule is not its business.
        self.client.open.side_effect = [Response({"token": google()})]
        self.assertIsNone(self.run_cli("mint", str(self.tmp / "token")))
        self.assertEqual([call.args[0].full_url for call in self.client.open.call_args_list], [iam_url])

    # ---- bootstrap <api> <ingest>: the token mints, the running revision (before D286) answers 401 ------

    def run_main(self, *args, env=None):
        out, err = io.StringIO(), io.StringIO()
        env = {"SMOKE_USER_ID": owner, "SMOKE_SA_ID": sa_id} if env is None else env
        with patch.dict(os.environ, env, clear=True), patch.object(sys, "argv", [helper, *args]), \
                patch.object(sys, "stdout", out), patch.object(sys, "stderr", err):
            try:
                runpy.run_path(helper, run_name="__main__")
                code = None
            except SystemExit as stop:
                code = str(stop)
        return code, out.getvalue(), err.getvalue()

    def test_bootstrap_passes_on_a_401(self):
        token = google()
        self.client.open.side_effect = [Response({"token": token}), urllib.error.HTTPError(api, 401, "x", {}, Response(b"sensitive response body"))]
        before = set(os.listdir(self.tmp))
        code, out, err = self.run_main("bootstrap", api, ingest)
        self.assertIsNone(code)
        self.assertEqual(out, "✓ smoke-prod bootstrap: the running revision predates D286; the token mints\n")
        self.assertEqual(err, "")
        mint, read = (call.args[0] for call in self.client.open.call_args_list)
        self.assertEqual(mint.full_url, iam_url)
        self.assertEqual((read.full_url, read.get_method(), read.headers["Authorization"]), (api + "/v1/me", "GET", "Bearer " + token))
        self.assertEqual(set(os.listdir(self.tmp)), before)  # no session file anywhere
        self.assertNotIn(token, out + err)

    def test_bootstrap_normalises_the_origin_like_every_other_mode(self):
        self.client.open.side_effect = [Response({"token": google()}), urllib.error.HTTPError(api, 401, "x", {}, Response(b""))]
        code, _, _ = self.run_main("bootstrap", "https://API.ryftlab.de:443/", ingest)
        self.assertIsNone(code)
        self.assertEqual(self.client.open.call_args.args[0].full_url, api + "/v1/me")

    def test_bootstrap_refuses_a_200_the_bootstrap_is_over(self):
        token = google()
        self.client.open.side_effect = [Response({"token": token}), Response(me())]
        code, out, err = self.run_main("bootstrap", api, ingest)
        self.assertEqual(code, "smoke-prod: the running api accepts the smoke token: the bootstrap is over (the deploy runs the preflight)")
        self.assertNotIn("auth_preflight", code)  # that input is gone: the deploy picks the preflight by itself
        self.assertEqual(out + err, "")
        self.assertEqual(self.client.open.call_count, 2)

    def test_bootstrap_fails_on_any_other_status(self):
        for status in (403, 404, 429, 500, 502, 307):  # 503 has its own tests: it is retried first
            with self.subTest(status=status):
                token = google()
                self.client.open.side_effect = [Response({"token": token}), urllib.error.HTTPError(api, status, "x", {}, Response(b"sensitive response body"))]
                code, out, err = self.run_main("bootstrap", api, ingest)
                self.assertEqual(code, f"smoke-prod: GET /v1/me returned HTTP {status}, the bootstrap expects 401 from a revision before D286")
                self.assertEqual(out, "")
                self.assertNotIn("sensitive response body", code + err)
        self.client.open.side_effect = [Response({"token": google()}), OSError("sensitive response body " + access)]
        code, out, _ = self.run_main("bootstrap", api, ingest)
        self.assertEqual((code, out), ("smoke-prod: GET /v1/me could not complete", ""))

    def test_bootstrap_proves_the_mint_first(self):
        self.client.open.side_effect = google_error(403, {"error": {"status": "PERMISSION_DENIED", "message": access}})
        code, out, err = self.run_main("bootstrap", api, ingest)
        self.assertIn("generateIdToken for the smoke account returned HTTP 403 (PERMISSION_DENIED); the gcloud account needs", code)
        self.assertEqual(self.client.open.call_count, 1)  # no /v1/me without a token
        self.assertNotIn(access, code + out + err)
        self.client.open.reset_mock()
        token = google(aud="other")
        self.client.open.side_effect = [Response({"token": token})]
        code, _, _ = self.run_main("bootstrap", api, ingest)
        self.assertEqual(code, "smoke-prod: minted token has the wrong audience")
        self.assertEqual(self.client.open.call_count, 1)

    def test_bootstrap_needs_canonical_origins_and_ignores_smoke_token(self):
        for given_api, given_ingest in ((remote_api, remote_ingest), ("https://api.ryftlab.de.evil.test", ingest), (api, ingest + ":8443"),
                                         ("http://localhost:8082", "http://127.0.0.1:8081")):
            with self.subTest(api=given_api, ingest=given_ingest):
                code, _, _ = self.run_main("bootstrap", given_api, given_ingest, env={
                    "SMOKE_USER_ID": owner, "SMOKE_SA_ID": sa_id, "SMOKE_TOKEN": bearer(),
                })
                self.assertRegex(code, "no smoke token is minted for this target|must be HTTPS")
        self.no_calls()
        self.client.open.side_effect = [Response({"token": google()}), urllib.error.HTTPError(api, 401, "x", {}, Response(b""))]
        self.run_main("bootstrap", api, ingest, env={"SMOKE_USER_ID": owner, "SMOKE_SA_ID": sa_id, "SMOKE_TOKEN": "ignored"})
        self.assertEqual(self.client.open.call_count, 2)  # it minted anyway

    # ---- GET /v1/me retries a 503 (a new instance loading its keys) and nothing else -------------------

    def unavailable(self):
        return urllib.error.HTTPError(api, 503, "x", {}, Response({"error": "smoke_jwks_unavailable"}))

    def test_me_retries_503_with_the_pauses_and_then_succeeds(self):
        for attempts in (1, 2, 5):
            with self.subTest(attempts=attempts):
                self.sleep.reset_mock()
                self.client.open.reset_mock()
                self.client.open.side_effect = [Response({"token": google()})] + [self.unavailable() for _ in range(attempts - 1)] + [Response(me())]
                self.prepare()
                self.assertEqual(self.client.open.call_count, 1 + attempts)
                self.assertEqual([call.args[0] for call in self.sleep.call_args_list], [2, 4, 4, 4][:attempts - 1])
                self.session.unlink()
        self.assertEqual(self.gcloud.call_count, 3)  # one mint per subtest

    def test_me_gives_up_after_five_503s(self):
        self.client.open.side_effect = [Response({"token": google()})] + [self.unavailable() for _ in range(5)]
        self.rejected(r"GET /v1/me returned HTTP 503$")
        self.assertEqual(self.client.open.call_count, 1 + 5)  # the mint and exactly five reads
        self.assertEqual([call.args[0] for call in self.sleep.call_args_list], [2, 4, 4, 4])

    def test_me_does_not_retry_anything_but_503(self):
        for status in (401, 403, 404, 429, 500, 502, 504):
            with self.subTest(status=status):
                self.sleep.reset_mock()
                self.client.open.reset_mock()
                self.client.open.side_effect = [Response({"token": google()}), urllib.error.HTTPError(api, status, "x", {}, Response(b""))]
                self.rejected(r"GET /v1/me returned HTTP %d$" % status)
                self.assertEqual(self.client.open.call_count, 2)
                self.sleep.assert_not_called()
        self.client.open.reset_mock()
        self.client.open.side_effect = [Response({"token": google()}), OSError("down")]
        self.rejected("GET /v1/me could not complete")
        self.sleep.assert_not_called()

    def test_run_retries_503_too(self):
        self.client.open.side_effect = [Response({"token": google()}), self.unavailable(), Response(me())]
        with patch.dict(os.environ, {}, clear=True):
            verify("run", api, ingest, self.result)
        self.assertEqual([call.args[0] for call in self.sleep.call_args_list], [2])
        self.assertEqual(json.loads(self.result.read_bytes())["me"], me())

    def test_bootstrap_retries_503_then_sees_the_expected_401(self):
        denied = urllib.error.HTTPError(api, 401, "x", {}, Response(b""))
        self.client.open.side_effect = [Response({"token": google()}), self.unavailable(), self.unavailable(), denied]
        code, out, _ = self.run_main("bootstrap", api, ingest)
        self.assertIsNone(code)
        self.assertTrue(out.startswith("✓ smoke-prod bootstrap:"))
        self.assertEqual([call.args[0] for call in self.sleep.call_args_list], [2, 4])

    def test_bootstrap_still_fails_on_a_persistent_503_and_never_treats_it_as_401(self):
        self.client.open.side_effect = [Response({"token": google()})] + [self.unavailable() for _ in range(5)]
        code, out, _ = self.run_main("bootstrap", api, ingest)
        self.assertEqual(code, "smoke-prod: GET /v1/me returned HTTP 503, the bootstrap expects 401 from a revision before D286")
        self.assertEqual(out, "")
        self.assertEqual(self.client.open.call_count, 1 + 5)
        self.assertEqual([call.args[0] for call in self.sleep.call_args_list], [2, 4, 4, 4])

    def test_bootstrap_a_200_after_a_503_is_still_over(self):
        self.client.open.side_effect = [Response({"token": google()}), self.unavailable(), Response(me())]
        code, _, _ = self.run_main("bootstrap", api, ingest)
        self.assertRegex(code, "the bootstrap is over")

    def test_bootstrap_needs_two_origins(self):
        for args in (("bootstrap",), ("bootstrap", api), ("bootstrap", api, ingest, "x")):
            with self.subTest(args=args):
                self.assertEqual(self.run_main(*args)[0], "smoke-prod: invalid auth helper invocation")
        self.no_calls()


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

# A lookalike origin (API_URL or INGEST_URL) stops before gcloud or curl is ever started, in every mode.
# The fake gcloud always fails, so a missed refusal still cannot reach the network.
: > "$tmp/gcloud.log"
: > "$tmp/curl.log"
cat > "$tmp/bin/gcloud" <<'SH'
#!/usr/bin/env bash
echo "gcloud $*" >> "$GCLOUD_LOG"
echo 'ya29.fixture-secret-marker'
exit 1
SH
cat > "$tmp/bin/curl" <<'SH'
#!/usr/bin/env bash
echo "curl $*" >> "$CURL_LOG"
exit 91
SH
chmod +x "$tmp/bin/gcloud" "$tmp/bin/curl"
smoke_env=(SMOKE_USER_ID='20261125-0000-4000-8000-0000000000a1' SMOKE_SA_ID='104567890123456789012')
for flag in none --auth-preflight --auth-bootstrap; do
  args=()
  [ "$flag" = none ] || args=("$flag")
  for target in API_URL=https://api.ryftlab.de.evil.test API_URL=https://api.ryftlab.de:8443 API_URL=http://api.ryftlab.de \
      API_URL=https://api.ryftlab.de/v1 INGEST_URL=https://ingest.ryftlab.de.evil.test INGEST_URL=https://ingest.ryftlab.de:8443; do
    if env -i PATH="$tmp/bin:$PATH" GCLOUD_LOG="$tmp/gcloud.log" CURL_LOG="$tmp/curl.log" "$target" "${smoke_env[@]}" \
        bash "$HERE/scripts/smoke_prod.sh" ${args[@]+"${args[@]}"} > "$tmp/lookalike" 2>&1; then
      echo "smoke-prod accepted $target ($flag)" >&2
      exit 1
    fi
    grep -q '^smoke-prod: ' "$tmp/lookalike" || { echo "no clean refusal for $target ($flag)" >&2; cat "$tmp/lookalike" >&2; exit 1; }
    if grep -q 'fixture-secret-marker' "$tmp/lookalike"; then echo "leaked a token for $target ($flag)" >&2; exit 1; fi
  done
done
[ ! -s "$tmp/gcloud.log" ] || { echo 'gcloud was started for a lookalike origin' >&2; exit 1; }
[ ! -s "$tmp/curl.log" ] || { echo 'curl was started for a lookalike origin' >&2; exit 1; }

# The canonical default reaches gcloud in both pre-rollout modes, and neither needs a session file.
for flag in --auth-preflight --auth-bootstrap; do
  : > "$tmp/gcloud.log"
  if env -i PATH="$tmp/bin:$PATH" GCLOUD_LOG="$tmp/gcloud.log" CURL_LOG="$tmp/curl.log" "${smoke_env[@]}" \
      bash "$HERE/scripts/smoke_prod.sh" "$flag" > "$tmp/wired" 2>&1; then
    echo "smoke-prod $flag passed with a failing gcloud" >&2
    exit 1
  fi
  grep -qx 'gcloud auth print-access-token' "$tmp/gcloud.log"
  grep -qx 'smoke-prod: gcloud auth print-access-token gave no access token (run gcloud auth login once)' "$tmp/wired"
done

# Unknown flags and extra arguments.
for args in '--nope' '--auth-bootstrap extra' '--auth-preflight extra'; do
  # shellcheck disable=SC2086  # word splitting is the point
  if env -i PATH="$tmp/bin:$PATH" bash "$HERE/scripts/smoke_prod.sh" $args > "$tmp/usage" 2>&1; then echo "accepted: $args" >&2; exit 1; fi
  grep -q 'usage: smoke_prod.sh \[--auth-preflight | --auth-bootstrap\]' "$tmp/usage"
done

# Every curl made with the smoke token retries curl's transient statuses, and only those. The real definitions
# of bearer, smoke_curl and call are run here against a fake curl that records its arguments.
cat > "$tmp/bin/curl" <<'SH'
#!/usr/bin/env bash
cat > /dev/null   # the header file arrives on stdin (-H @-)
echo "$*" >> "$CURL_LOG"
SH
chmod +x "$tmp/bin/curl"
: > "$tmp/curl.log"
{
  sed -n '/^bearer() {/,/^}/p' "$HERE/scripts/smoke_prod.sh"
  grep '^smoke_curl()' "$HERE/scripts/smoke_prod.sh"
  grep '^call()' "$HERE/scripts/smoke_prod.sh"
  echo 'TOKEN=fixture-token; call -X POST http://x.test/a; smoke_curl -s http://x.test/b'
} > "$tmp/defs.sh"
env -i PATH="$tmp/bin:$PATH" CURL_LOG="$tmp/curl.log" bash "$tmp/defs.sh"
[ "$(wc -l < "$tmp/curl.log")" -eq 2 ]
[ "$(grep -c -e '--retry 3 --retry-delay 2' "$tmp/curl.log")" -eq 2 ]
if grep -q -e '--retry-all-errors' "$tmp/curl.log"; then echo 'curl retries all errors' >&2; exit 1; fi
# No other call in the script sends TOKEN around smoke_curl, and nothing asks for --retry-all-errors.
if grep -v '^smoke_curl()' "$HERE/scripts/smoke_prod.sh" | grep -q 'bearer "[$]TOKEN"'; then
  echo 'a smoke-token call bypasses smoke_curl' >&2
  exit 1
fi
if grep -v '^ *#' "$HERE/scripts/smoke_prod.sh" | grep -q -e '--retry-all-errors'; then echo 'smoke_prod.sh retries all errors' >&2; exit 1; fi

# D286: the smoke token writes no consent. The api refuses POST and DELETE /v1/me/consent for it, so the script
# must never send either, and a missing or stale consent is a failure with the operator's hint, not a write.
# No command in the script (comments aside) names the consent route at all.
if grep -v '^ *#' "$HERE/scripts/smoke_prod.sh" | grep -q 'me/consent'; then
  echo 'smoke_prod.sh still names /v1/me/consent' >&2
  exit 1
fi

# The real smoke_prod.sh against a fake curl that records "METHOD host path" and a fake python3 that stands in for
# the auth helper's `run` (the helper itself is tested above) and passes everything else to the real one.
real_py=$(command -v python3)
mkdir "$tmp/fake"
cat > "$tmp/fake/python3" <<'SH'
#!/usr/bin/env bash
if [ "${1:-}" = -I ] && [ "${3:-}" = run ]; then
  printf '{"api": "%s", "ingest": "%s", "token": "fixture-token", "subject": "20261125-0000-4000-8000-0000000000a1", "me": %s}' \
    "$4" "$5" "$FAKE_ME" > "$6"
  exit 0
fi
exec "$REAL_PY" "$@"
SH
cat > "$tmp/fake/curl" <<'SH'
#!/usr/bin/env bash
cat > /dev/null   # the bearer header arrives on stdin (-H @-)
method=GET out= hdr= fmt= url= fail=
while [ $# -gt 0 ]; do
  case $1 in
    -X) method=$2; shift 2 ;;
    -o) out=$2; shift 2 ;;
    -D) hdr=$2; shift 2 ;;
    -w) fmt=$2; shift 2 ;;
    -H|-d|--retry|--retry-delay) shift 2 ;;
    -f|-fsS) fail=1; shift ;;
    http*) url=$1; shift ;;
    *) shift ;;
  esac
done
rest=${url#*://}
host=${rest%%/*}
path=/${rest#*/}
echo "$method $host $path" >> "$CURL_LOG"
status=200 body='{}'
case "$method $host $path" in
  "GET api.fake.test /v1/me") status=401 ;;
  "GET ingest.fake.test /v1/sync") status=404 ;;
  "GET api.fake.test /v1/config") body="{\"privacy_policy_version\": \"$FAKE_POLICY\"}" ;;
  "GET api.fake.test /v1/model") body='{"current": "m1"}' ;;
  "GET api.fake.test /v1/me/connections") body='{"connections": [], "available": [], "providers": [{"provider": "garmin"}, {"provider": "suunto"}]}' ;;
  "POST ingest.fake.test /v1/sync") body="{\"days_touched\": [\"$(date -u +%F)\"]}" ;;
  "GET api.fake.test /v1/days/"*) body='{"score": {"computed_at": "9999-01-01T00:00:00", "model_version": "m1"}}' ;;
  "GET api.fake.test /v1/days")
    if [ "$FAKE_INVITE" = on ]; then status=403 body='{"error": "forbidden", "code": "invite_required"}'; fi ;;
esac
[ -z "$hdr" ] || printf 'HTTP/1.1 %s\n' "$status" > "$hdr"
if [ -n "$out" ] && [ "$out" != /dev/null ]; then printf '%s' "$body" > "$out"; fi
if [ -n "$fail" ] && [ "$status" -ge 400 ]; then exit 22; fi
if [ -n "$fmt" ]; then printf '%s' "$status"; elif [ -z "$out" ]; then printf '%s' "$body"; fi
SH
chmod +x "$tmp/fake/python3" "$tmp/fake/curl"
fake_me() {  # consent JSON → the smoke account's /v1/me
  printf '{"id": "20261125-0000-4000-8000-0000000000a1", "beta_access": true, "tz": "UTC", "consent": %s}' "$1"
}
consent_now='{"policy_version": "2026-10-26", "granted_at": "2026-10-26T09:00:00Z"}'
smoke_fake() {  # NAME=value... → exit status of smoke_prod.sh; the requests are in $tmp/api.log, the output in $tmp/run.out
  : > "$tmp/api.log"
  env -i PATH="$tmp/fake:$PATH" REAL_PY="$real_py" CURL_LOG="$tmp/api.log" API_URL=http://api.fake.test \
    INGEST_URL=http://ingest.fake.test FAKE_POLICY=2026-10-26 FAKE_INVITE=off FAKE_ME="$(fake_me "$consent_now")" "$@" \
    bash "$HERE/scripts/smoke_prod.sh" < /dev/null > "$tmp/run.out" 2>&1
}
no_consent_write() {  # the smoke token neither grants nor withdraws consent: no request names it, nothing but the sync is written
  if grep -q 'consent' "$tmp/api.log"; then echo 'smoke_prod.sh sent a request about consent' >&2; cat "$tmp/api.log" >&2; exit 1; fi
  if grep -q '^DELETE api.fake.test /v1/me' "$tmp/api.log"; then echo 'smoke_prod.sh sent a DELETE to the api' >&2; exit 1; fi
}

# The consent is current: the smoke passes, reads no consent route, writes only the sync.
smoke_fake || { echo 'smoke_prod.sh failed with a current consent' >&2; cat "$tmp/run.out" >&2; exit 1; }
no_consent_write
[ "$(grep '^POST' "$tmp/api.log")" = 'POST ingest.fake.test /v1/sync' ] || { echo 'smoke_prod.sh wrote something but the sync' >&2; exit 1; }
grep -qF 'gates: invite unknown (no ADMIN_API_KEY), consent not probed (D286: the smoke token writes no consent)' "$tmp/run.out"

# The gate probe takes the beta access away and gives it back (admin key); it never touches consent.
smoke_fake ADMIN_API_KEY=fixture-admin FAKE_INVITE=on SMOKE_EXPECT_GATES=on \
  || { echo 'smoke_prod.sh failed with the invite gate on' >&2; cat "$tmp/run.out" >&2; exit 1; }
no_consent_write
[ "$(grep -c '^DELETE ' "$tmp/api.log")" -eq 1 ] || { echo 'the gate probe sent more than the one admin DELETE' >&2; exit 1; }
grep -q '^DELETE api.fake.test /v1/admin/users/.*/access$' "$tmp/api.log" || { echo 'the admin DELETE is not the access one' >&2; exit 1; }
grep -qF 'gates: invite on, consent not probed' "$tmp/run.out"

# SMOKE_EXPECT_GATES=on checks the invite gate only: off (or unknown) fails, and says so.
smoke_fake ADMIN_API_KEY=fixture-admin FAKE_INVITE=off SMOKE_EXPECT_GATES=on && { echo 'SMOKE_EXPECT_GATES=on passed with the invite gate off' >&2; exit 1; }
grep -qxF '✗ smoke-prod: SMOKE_EXPECT_GATES=on, but the invite gate is: off (the consent gate is not probed)' "$tmp/run.out"
no_consent_write
smoke_fake SMOKE_EXPECT_GATES=on && { echo 'SMOKE_EXPECT_GATES=on passed without a probe' >&2; exit 1; }
grep -qF 'but the invite gate is: unknown (no ADMIN_API_KEY)' "$tmp/run.out"

# A missing or stale consent fails with the operator's hint, before the sync, and the script writes nothing.
for stale in null '{"policy_version": "2026-01-01", "granted_at": "2026-01-01T00:00:00Z"}' '{"policy_version": "2026-12-01"}'; do
  smoke_fake FAKE_ME="$(fake_me "$stale")" && { echo "smoke_prod.sh passed with the consent $stale" >&2; exit 1; }
  grep -qxF '✗ smoke-prod: the smoke account has no consent for 2026-10-26: record it with scripts/sql/consent_smoke_account.sql (docs/OPERATIONS.md)' "$tmp/run.out" \
    || { echo "no consent message for $stale" >&2; cat "$tmp/run.out" >&2; exit 1; }
  no_consent_write
  if grep -q '^POST' "$tmp/api.log"; then echo "smoke_prod.sh wrote with the consent $stale" >&2; exit 1; fi
done
# The comparison is with /v1/config's version, whatever it is.
smoke_fake FAKE_POLICY=2026-12-01 && { echo 'smoke_prod.sh passed with a consent for another policy than /v1/config' >&2; exit 1; }
grep -qF 'no consent for 2026-12-01: record it with' "$tmp/run.out"
no_consent_write
