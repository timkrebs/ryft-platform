import os
import unittest
from unittest.mock import patch
from urllib.parse import quote

from import_pg import SUPABASE_CA, connect


HOST = "aws-1-eu-central-1.pooler.supabase.com"
PROJECT = "abcdefghijklmnopqrst"
USER = f"nutrition_etl_login.{PROJECT}"
PASSWORD = "synthetic secret/@"
URI = f"postgresql://{USER}:{quote(PASSWORD, safe='')}@{HOST}:5432/postgres?sslmode=verify-full"
HOSTED = {"CLOUD_RUN_JOB": "test", "SUPABASE_POOLER_HOST": HOST, "SUPABASE_PROJECT_REF": PROJECT}


class ConnectionTests(unittest.TestCase):
    def test_hosted_dsn_uses_approved_settings_and_bundled_ca(self):
        keyword_dsn = (
            f"host={HOST} port=5432 dbname=postgres user={USER} password='{PASSWORD}' "
            f"sslmode=verify-full sslrootcert={SUPABASE_CA}"
        )
        for dsn in (URI, keyword_dsn):
            with self.subTest(format=dsn.startswith("postgresql://")), patch.dict(
                os.environ, {**HOSTED, "DATABASE_URL": dsn, "PGSSLROOTCERT": "/tmp/unapproved"}, clear=True
            ), patch("import_pg.psycopg.connect") as open_connection:
                self.assertIs(connect(), open_connection.return_value)
                open_connection.assert_called_once_with(
                    autocommit=True, connect_timeout=10, application_name="nutrition-etl", client_encoding="UTF8",
                    host=HOST, port="5432", dbname="postgres", user=USER, password=PASSWORD, sslmode="verify-full",
                    sslrootcert=SUPABASE_CA, hostaddr="", gssencmode="disable",
                )

    def test_unapproved_hosted_settings_stop_before_connection(self):
        cases = {
            "unconfigured host": (URI, {"SUPABASE_POOLER_HOST": ""}),
            "unconfigured project": (URI, {"SUPABASE_PROJECT_REF": ""}),
            "invalid dsn": ("password='synthetic secret", {}),
            "different host": (URI.replace(HOST, "wrong.invalid"), {}),
            "different port": (URI.replace(":5432/", ":6543/"), {}),
            "alternate address": (URI + "&hostaddr=192.0.2.1", {}),
            "alternate route": (URI.replace(HOST, f"{HOST},wrong.invalid"), {}),
            "different project": (URI.replace(PROJECT, "zyxwvutsrqponmlkjihg"), {}),
            "different database": (URI.replace("/postgres?", "/other?"), {}),
            "certificate override": (URI + "&sslrootcert=/tmp/unapproved", {}),
            "ambient service": (URI, {"PGSERVICE": "unapproved"}),
            "dsn service": (URI + "&service=unapproved", {}),
            "without sslmode": (URI.split("?")[0], {}),
        }
        for mode in ("disable", "allow", "prefer", "require", "verify-ca"):
            cases[mode] = (URI.replace("verify-full", mode), {})
        for name, (dsn, override) in cases.items():
            with self.subTest(name=name), patch.dict(
                os.environ, {**HOSTED, **override, "DATABASE_URL": dsn}, clear=True
            ), patch("import_pg.psycopg.connect") as open_connection:
                with self.assertRaises(ValueError) as error:
                    connect()
                self.assertNotIn("synthetic", str(error.exception))
                self.assertNotIn(dsn, str(error.exception))
                open_connection.assert_not_called()

    def test_cloud_run_service_marker_enforces_the_same_policy(self):
        with patch.dict(os.environ, {"K_SERVICE": "test", "DATABASE_URL": "host=somewhere"}, clear=True), patch(
            "import_pg.psycopg.connect"
        ) as open_connection:
            with self.assertRaisesRegex(ValueError, "approved Supabase"):
                connect()
            open_connection.assert_not_called()

    def test_cloud_sql_socket_and_local_dsn_are_unchanged(self):
        instance = "test-project:europe-west1:test-db"
        with patch.dict(os.environ, {
            "CLOUD_RUN_JOB": "test", "INSTANCE_CONNECTION_NAME": instance,
            "PGUSER": "etl", "PGDATABASE": "catalog",
        }, clear=True), patch("import_pg.psycopg.connect") as open_connection:
            connect()
            open_connection.assert_called_once_with(
                host=f"/cloudsql/{instance}", hostaddr="", port=5432, user="etl", dbname="catalog",
                autocommit=True, connect_timeout=10, application_name="nutrition-etl", client_encoding="UTF8",
            )

        dsn = "postgresql://etl@127.0.0.1:5432/catalog?sslmode=disable"
        with patch.dict(os.environ, {"DATABASE_URL": dsn}, clear=True), patch(
            "import_pg.psycopg.connect"
        ) as open_connection:
            connect()
            open_connection.assert_called_once_with(
                dsn, autocommit=True, connect_timeout=10, application_name="nutrition-etl", client_encoding="UTF8",
            )
