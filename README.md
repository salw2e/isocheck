# isocheck

Checks what transaction isolation a database really gives. It runs concurrent transactions against a Postgres or MySQL compatible database, records what every client saw, and looks for anomalies the way Jepsen's [Elle](https://github.com/jepsen-io/elle) does: it works out the order of writes from the reads, builds the dependency graph between transactions and searches it for cycles. No JVM needed.

## Install

```
go install github.com/salw2e/isocheck@latest
```

## Usage

```
isocheck -isolation serializable postgres://user@localhost:5432/db
isocheck -isolation repeatable-read mysql://root@localhost:3306/db
isocheck check history.edn
```

It drops and creates a table called `isocheck` (`-table` to change it). `-save h.edn` keeps the history in the format Elle reads, and `isocheck check` checks a list-append history from Jepsen or Elle. The exit code is 1 when the database falls short of the level it was asked for (or `-expect`).

```
$ isocheck -isolation repeatable-read postgres://postgres@localhost:5432/x
2000 transactions: 1151 committed, 849 aborted, 0 unknown

G2-item, found 35 times: write skew: transactions each missed the other's write
  rw T1938 read key 75 = [2293 2326 2327 2357 2389], missing 2410 appended next by T1949
  rw T1949 read key 78 = [2379], missing 2401 appended next by T1941
  rw T1941 read key 74 = [2250 2309 2313 2338 2354 2366], missing 2397 appended next by T1938
  ...

read-committed       yes
snapshot-isolation   yes
repeatable-read      no   G2-item
serializable         no   G2-item
strict-serializable  no   G2-item, G2-item-realtime

Not repeatable-read.
```

## Results

3,000 transactions from 10 clients at each level.

| Database | Level | Strongest model it met | Anomalies |
|---|---|---|---|
| Postgres 17 | SERIALIZABLE | strict serializable | none |
| Postgres 17 | REPEATABLE READ | snapshot isolation | G2-item |
| Postgres 17 | READ COMMITTED | read committed | G-single, G2-item, non-repeatable reads |
| MySQL 8.4 | SERIALIZABLE | strict serializable | none |
| MySQL 8.4 | REPEATABLE READ | read committed | G-single, G2-item, non-repeatable reads |
| MySQL 8.4 | READ COMMITTED | read committed | G-single, G2-item, non-repeatable reads |

MySQL's REPEATABLE READ is not snapshot isolation: a write reads the latest committed row, so a later read in the same transaction sees what other transactions committed in between.

## Checking the checker

- Every anomaly has a small hand-built history in the tests, and breaking the checker on purpose (no real-time edges, no G1b check, a one-rw cycle named G2-item) fails them.
- On 30 histories from Postgres and MySQL at every level, isocheck and Elle gave the same verdict for all 144 checks where Elle reached one. Elle timed out on the 3 others (large read committed histories, which isocheck checks in 0.2 s).
- A build of [isodb](https://github.com/salw2e/isodb) with its serializable checks switched off is caught at SERIALIZABLE (G2-item).

## Anomalies

G0 write cycle, G1a aborted read, G1b intermediate read, G1c circular information flow, G-single read skew, G2-item write skew, after Adya. A `-realtime` suffix means the cycle needs real-time order, so only strict serializability rules it out. Read committed forbids G0 and G1, snapshot isolation adds G-single, repeatable read and serializable add G2-item.
