# isocheck

Tests which transaction isolation level a Postgres or MySQL database actually provides, using the same method as Jepsen's [Elle](https://github.com/jepsen-io/elle).

```
go install github.com/salw2e/isocheck@latest
isocheck -isolation serializable postgres://user@localhost:5432/db
isocheck -isolation repeatable-read mysql://root@localhost:3306/db
```

It creates a table called `isocheck` in the database. Exit code is 1 if the level isn't met.

```
read-committed       yes
snapshot-isolation   yes
repeatable-read      no   G2-item
serializable         no   G2-item
```

## Results

| Database | SERIALIZABLE | REPEATABLE READ |
|---|---|---|
| Postgres 17 | serializable | snapshot isolation |
| MySQL 8.4 | serializable | read committed |
| MariaDB 10.11 | serializable | read committed |
| MariaDB 11.8 | serializable | snapshot isolation |
| TiDB 8.5 | not supported | read committed |
| CockroachDB 26.3 | serializable | runs as serializable |
| YugabyteDB 2026.1 | serializable | snapshot isolation |
