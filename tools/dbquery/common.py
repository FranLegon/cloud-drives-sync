#!/usr/bin/env python3
"""Shared helpers for encrypted metadata database exploration scripts."""

from __future__ import annotations

import os
import sys

try:
    import sqlcipher3 as sqlcipher
except ImportError:  # pragma: no cover - fallback for pysqlcipher3
    try:
        from pysqlcipher3 import dbapi2 as sqlcipher
    except ImportError:
        print(
            "Missing dependency: install a SQLCipher driver, e.g. `pip install pysqlcipher3` "
            "or a compatible `sqlcipher3` package for your Python version/platform."
        )
        sys.exit(1)

DB_FILE = "cloud-drives-sync-metadata.db"
PASSWORD_ENV_VAR = "CLOUD_DRIVES_SYNC_PASS"


def get_db_password() -> str:
    pass_ = os.environ.get(PASSWORD_ENV_VAR)
    if not pass_:
        print(f"{PASSWORD_ENV_VAR} not set")
        sys.exit(1)
    return pass_


def connect_db(db_file: str = DB_FILE):
    pass_ = get_db_password()
    conn = sqlcipher.connect(db_file)
    # Escape single quotes for the PRAGMA key literal.
    conn.execute("PRAGMA key = '%s'" % pass_.replace("'", "''"))
    conn.execute("PRAGMA journal_mode = WAL")
    conn.execute("PRAGMA synchronous = NORMAL")
    conn.execute("PRAGMA busy_timeout = 5000")
    return conn
