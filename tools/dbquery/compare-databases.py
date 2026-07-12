#!/usr/bin/env python3
"""Compare two encrypted SQLCipher metadata databases and print all differences."""

from __future__ import annotations

import argparse
import json
import sys
from collections import defaultdict
from typing import Any

from common import connect_db


CHECKSUM_TABLE_ORDER = [
    "files",
    "replicas",
    "replica_fragments",
    "folders",
    "logical_folders",
    "folder_replicas",
]


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


def get_table_info(conn, table_name: str) -> list[tuple[Any, ...]]:
    return conn.execute(f"PRAGMA table_info({quote_identifier(table_name)})").fetchall()


def get_table_columns(conn, table_name: str) -> list[str]:
    return [row[1] for row in get_table_info(conn, table_name)]


def get_comparable_columns(conn, table_name: str) -> list[str]:
    columns: list[str] = []
    for _, name, column_type, _, _, pk in get_table_info(conn, table_name):
        if should_exclude_column(table_name, name, column_type, pk):
            continue
        columns.append(name)
    return columns


def should_exclude_column(
    table_name: str, column_name: str, column_type: str | None, pk: int
) -> bool:
    if column_name == "last_seen_at":
        return True
    if table_name == "folders" and column_name == "user_email":
        return True
    if column_name == "id" and pk > 0 and is_integer_column_type(column_type):
        return True
    return False


def is_integer_column_type(column_type: str | None) -> bool:
    return column_type is not None and "INT" in column_type.strip().upper()


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


def format_value(value: Any) -> str:
    if value is None:
        return "null"
    if isinstance(value, dict) and set(value) == {"__bytes__"}:
        return json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(",", ":"))
    if isinstance(value, bool):
        return "1" if value else "0"
    return str(value)


def format_row(row: dict[str, Any]) -> str:
    return "|".join(f"{column}:{format_value(row[column])}" for column in sorted(row))


def load_table_rows(conn, table_name: str) -> list[dict[str, Any]]:
    columns = get_comparable_columns(conn, table_name)
    if not columns:
        return []
    query = "SELECT {} FROM {}".format(
        ", ".join(quote_identifier(column) for column in columns),
        quote_identifier(table_name),
    )
    rows = conn.execute(query).fetchall()
    comparable_rows = [row_to_comparable_dict(columns, row) for row in rows]
    comparable_rows.sort(key=format_row)
    return comparable_rows


def diff_rows(
    left_rows: list[dict[str, Any]], right_rows: list[dict[str, Any]]
) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    left_counts: dict[str, list[dict[str, Any]]] = defaultdict(list)
    right_counts: dict[str, list[dict[str, Any]]] = defaultdict(list)

    for row in left_rows:
        left_counts[format_row(row)].append(row)
    for row in right_rows:
        right_counts[format_row(row)].append(row)

    left_only: list[dict[str, Any]] = []
    right_only: list[dict[str, Any]] = []

    for key in sorted(set(left_counts) | set(right_counts)):
        left_group = left_counts.get(key, [])
        right_group = right_counts.get(key, [])
        shared_count = min(len(left_group), len(right_group))
        left_only.extend(left_group[shared_count:])
        right_only.extend(right_group[shared_count:])

    left_only.sort(key=format_row)
    right_only.sort(key=format_row)
    return left_only, right_only


def print_row_block(title: str, rows: list[dict[str, Any]]) -> None:
    print(title)
    for row in rows:
        print(format_row(row))


def compare_databases(left_db: str, right_db: str) -> int:
    left_conn = connect_db(left_db)
    right_conn = connect_db(right_db)

    try:
        left_tables = set(get_table_names(left_conn))
        right_tables = set(get_table_names(right_conn))

        differences_found = False

        left_only_tables = sorted(left_tables - right_tables)
        right_only_tables = sorted(right_tables - left_tables)
        ordered_common_tables = [
            table_name
            for table_name in CHECKSUM_TABLE_ORDER
            if table_name in left_tables and table_name in right_tables
        ]
        remaining_common_tables = sorted(
            (left_tables & right_tables) - set(ordered_common_tables)
        )
        common_tables = ordered_common_tables + remaining_common_tables

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
            left_columns = get_comparable_columns(left_conn, table_name)
            right_columns = get_comparable_columns(right_conn, table_name)

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
                print(f"LEFT COLUMNS   : {left_columns}")
                print(f"RIGHT COLUMNS  : {right_columns}")
                print(f"LEFT ROW COUNT : {len(left_rows)}")
                print(f"RIGHT ROW COUNT: {len(right_rows)}")
                print(f"LEFT ONLY ROWS : {len(left_only_rows)}")
                print(f"RIGHT ONLY ROWS: {len(right_only_rows)}")
                print()
                if left_only_rows:
                    print_row_block("-- LEFT ONLY --", left_only_rows)
                if right_only_rows:
                    print_row_block("-- RIGHT ONLY --", right_only_rows)
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


