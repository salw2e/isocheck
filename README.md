# isocheck

Checks what transaction isolation a Postgres or MySQL compatible database actually gives you. It runs concurrent transactions, records what each client read and wrote, then builds the dependency graph between transactions and looks for cycles, the same method as Jepsen's [Elle](https://github.com/jepsen-io/elle). No JVM needed.

```
go install github.com/salw2e/isocheck@latest

isocheck -isolation serializable postgres://user@localhost:5432/db
isocheck -isolation repeatable-read mysql://root@localhost:3306/db
isocheck check history.edn
```

It drops and creates a table called `isocheck` (`-table` to change it). `-save h.edn` keeps the history in Elle's format, and `isocheck check` reads a list-append history from Jepsen or Elle. Exit code is 1 when the database doesn't meet the level it was asked for.

```
$ isocheck -isolation repeatable-read postgres://postgres@localhost:5432/x
2000 transactions: 1151 committed, 849 aborted, 0 unknown

G2-item, found 35 times: write skew: transactions each missed the other's write
  rw T1938 read key 75 = [2293 2326 2327 2357 2389], missing 2410 appended next by T1949
  ...

read-committed       yes
snapshot-isolation   yes
repeatable-read      no   G2-item
serializable         no   G2-item
strict-serializable  no   G2-item, G2-item-realtime
```

## Results

3,000 transactions from 10 clients (1,000 on YugabyteDB), single node, default settings.

| Database | SERIALIZABLE | REPEATABLE READ | READ COMMITTED |
|---|---|---|---|
| Postgres 17 | strict serializable | snapshot isolation | read committed |
| MySQL 8.4 | strict serializable | read committed | read committed |
| MariaDB 10.11 | strict serializable | read committed | read committed |
| MariaDB 11.8 | strict serializable | snapshot isolation | read committed |
| TiDB 8.5, pessimistic (default) | not supported | read committed | read committed |
| TiDB 8.5, optimistic | | snapshot isolation | |
| CockroachDB 26.3 | strict serializable | runs as SERIALIZABLE | read committed |
| CockroachDB 26.3, REPEATABLE READ enabled | | snapshot isolation | |
| YugabyteDB 2026.1 | strict serializable | snapshot isolation | read committed |

MySQL, MariaDB 10.11 and pessimistic TiDB give only read committed at REPEATABLE READ, because a write there reads the latest committed row instead of the snapshot. MariaDB 11.8 fixed this with `innodb_snapshot_isolation`, which is on by default.

How it was checked against Elle and the list of anomalies it detects are in [docs/notes.md](docs/notes.md).
