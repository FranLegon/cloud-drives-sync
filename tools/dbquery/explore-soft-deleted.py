#!/usr/bin/env python3
"""Query the encrypted SQLCipher metadata database and print a status report."""

from common import connect_db


def main():
    conn = connect_db()

    try:
        print("=== FILES (logical) ===")
        for path, name, status in conn.execute(
            "SELECT path, name, status FROM files ORDER BY status, path"
        ):
            print("[%-12s] %s" % (status, path))

        print("\n=== FILE STATUS COUNTS ===")
        for status, c in conn.execute(
            "SELECT status, COUNT(*) FROM files GROUP BY status"
        ):
            print("%-12s %d" % (status, c))

        print("\n=== REPLICAS by provider/status (active, by path location) ===")
        for provider, status, in_soft, in_root in conn.execute(
            """
            SELECT provider, status,
                SUM(CASE WHEN path LIKE '%soft-deleted%' THEN 1 ELSE 0 END) as in_soft,
                SUM(CASE WHEN path NOT LIKE '%soft-deleted%' THEN 1 ELSE 0 END) as in_root
            FROM replicas GROUP BY provider, status ORDER BY provider, status
            """
        ):
            print(
                "%-10s %-10s soft-deleted=%d  active-area=%d"
                % (provider, status, in_soft, in_root)
            )

        print("\n=== ACTIVE replicas NOT in soft-deleted (these are 'at root/active') ===")
        n = 0
        for provider, account, path in conn.execute(
            """
            SELECT provider, account_id, path FROM replicas
            WHERE status='active' AND path NOT LIKE '%soft-deleted%'
            ORDER BY provider, path
            """
        ):
            print("%-10s %-30s %s" % (provider, account, path))
            n += 1
        print("TOTAL active-area replicas: %d" % n)
    finally:
        conn.close()


if __name__ == "__main__":
    main()
