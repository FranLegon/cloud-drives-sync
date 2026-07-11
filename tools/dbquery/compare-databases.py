#!/usr/bin/env python3
"""Compare two encrypted SQLCipher metadata databases and print all differences."""

from __future__ import annotations

import argparse
import json
import sys
from collections import defaultdict
from typing import Any

from common import connect_db


def get_table_names(conn) -> list[str]:
    rows = conn.execute(
        """
        SELECT name
        FROM sqlite_master
        WHERE type = 'table'
          AND name NOT LIKE 'sqlite_%'
        ORDER BY name
        """
    ).fetchall()
    return [row[0] for row in rows]


def get_table_columns(conn, table_name: str) -> list[str]:
    rows = conn.execute(f"PRAGMA table_info({quote_identifier(table_name)})").fetchall()
    return [row[1] for row in rows]


def quote_identifier(value: str) -> str:
    return '"' + value.replace('"', '""') + '"'


def normalize_value(value: Any) -> Any:
    if isinstance(value, bytes):
        return {"__bytes__": value.hex()}
    return value


def row_to_comparable_dict(columns: list[str], row: tuple[Any, ...]) -> dict[str, Any]:
    return {
        column: normalize_value(value)
        for column, value in zip(columns, row, strict=False)
    }


def canonicalize_row(row: dict[str, Any]) -> str:
    return json.dumps(row, sort_keys=True, ensure_ascii=False, separators=(",", ":"))


def load_table_rows(conn, table_name: str) -> list[dict[str, Any]]:
    columns = get_table_columns(conn, table_name)
    query = f"SELECT * FROM {quote_identifier(table_name)}"
    rows = conn.execute(query).fetchall()
    comparable_rows = [row_to_comparable_dict(columns, row) for row in rows]
    comparable_rows.sort(key=canonicalize_row)
    return comparable_rows


def diff_rows(
    left_rows: list[dict[str, Any]], right_rows: list[dict[str, Any]]
) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    left_counts: dict[str, list[dict[str, Any]]] = defaultdict(list)
    right_counts: dict[str, list[dict[str, Any]]] = defaultdict(list)

    for row in left_rows:
        left_counts[canonicalize_row(row)].append(row)
    for row in right_rows:
        right_counts[canonicalize_row(row)].append(row)

    left_only: list[dict[str, Any]] = []
    right_only: list[dict[str, Any]] = []

    for key in sorted(set(left_counts) | set(right_counts)):
        left_group = left_counts.get(key, [])
        right_group = right_counts.get(key, [])
        shared_count = min(len(left_group), len(right_group))
        left_only.extend(left_group[shared_count:])
        right_only.extend(right_group[shared_count:])

    left_only.sort(key=canonicalize_row)
    right_only.sort(key=canonicalize_row)
    return left_only, right_only


def print_json_block(title: str, rows: list[dict[str, Any]]) -> None:
    print(title)
    for row in rows:
        print(json.dumps(row, sort_keys=True, ensure_ascii=False, indent=2))


def compare_databases(left_db: str, right_db: str) -> int:
    left_conn = connect_db(left_db)
    right_conn = connect_db(right_db)

    try:
        left_tables = set(get_table_names(left_conn))
        right_tables = set(get_table_names(right_conn))

        differences_found = False

        left_only_tables = sorted(left_tables - right_tables)
        right_only_tables = sorted(right_tables - left_tables)
        common_tables = sorted(left_tables & right_tables)

        if left_only_tables:
            differences_found = True
            print("=== TABLES ONLY IN LEFT DATABASE ===")
            for table_name in left_only_tables:
                print(table_name)
            print()

        if right_only_tables:
            differences_found = True
            print("=== TABLES ONLY IN RIGHT DATABASE ===")
            for table_name in right_only_tables:
                print(table_name)
            print()

        for table_name in common_tables:
            left_columns = get_table_columns(left_conn, table_name)
            right_columns = get_table_columns(right_conn, table_name)

            if left_columns != right_columns:
                differences_found = True
                print(f"=== SCHEMA DIFFERENCE: {table_name} ===")
                print(f"LEFT COLUMNS : {left_columns}")
                print(f"RIGHT COLUMNS: {right_columns}")
                print()
                continue

            left_rows = load_table_rows(left_conn, table_name)
            right_rows = load_table_rows(right_conn, table_name)
            left_only_rows, right_only_rows = diff_rows(left_rows, right_rows)

            if left_only_rows or right_only_rows:
                differences_found = True
                print(f"=== ROW DIFFERENCES: {table_name} ===")
                print(f"LEFT ROW COUNT : {len(left_rows)}")
                print(f"RIGHT ROW COUNT: {len(right_rows)}")
                print(f"LEFT ONLY ROWS : {len(left_only_rows)}")
                print(f"RIGHT ONLY ROWS: {len(right_only_rows)}")
                print()
                if left_only_rows:
                    print_json_block("-- LEFT ONLY --", left_only_rows)
                if right_only_rows:
                    print_json_block("-- RIGHT ONLY --", right_only_rows)
                print()

        if not differences_found:
            print("No differences found.")
            return 0

        return 1
    finally:
        left_conn.close()
        right_conn.close()


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Compare two encrypted SQLCipher metadata databases."
    )
    parser.add_argument(
        "--left-db",
        dest="left_db",
        required=True,
        help="Path to the first database file",
    )
    parser.add_argument(
        "--right-db",
        dest="right_db",
        required=True,
        help="Path to the second database file",
    )
    return parser.parse_args()


if __name__ == "__main__":
    args = parse_args()
    sys.exit(compare_databases(args.left_db, args.right_db))


