package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type Config struct {
	URL       string
	Isolation string
	Clients   int
	Txns      int
	Keys      int // keys in play at once
	MaxWrites int // appends to a key before it is retired for a fresh one
	MaxOps    int // micro-ops per transaction, 1 to MaxOps
	Table     string
}

type dialect struct {
	driver, dsn            string
	create, append, read   string
	drop                   string
}

func dialectFor(c Config) (dialect, error) {
	u, err := url.Parse(c.URL)
	if err != nil {
		return dialect{}, err
	}
	t := c.Table
	switch u.Scheme {
	case "postgres", "postgresql":
		return dialect{
			driver: "pgx", dsn: c.URL,
			drop:   "DROP TABLE IF EXISTS " + t,
			create: "CREATE TABLE " + t + " (k integer PRIMARY KEY, v text)",
			append: "INSERT INTO " + t + " (k, v) VALUES ($1, $2) ON CONFLICT (k) DO UPDATE SET v = " + t + ".v || ',' || excluded.v",
			read:   "SELECT v FROM " + t + " WHERE k = $1",
		}, nil
	case "mysql":
		cfg := mysql.NewConfig()
		cfg.User = u.User.Username()
		cfg.Passwd, _ = u.User.Password()
		cfg.Net, cfg.Addr = "tcp", u.Host
		if u.Port() == "" {
			cfg.Addr += ":3306"
		}
		cfg.DBName = strings.TrimPrefix(u.Path, "/")
		return dialect{
			driver: "mysql", dsn: cfg.FormatDSN(),
			drop:   "DROP TABLE IF EXISTS " + t,
			create: "CREATE TABLE " + t + " (k int PRIMARY KEY, v text) ENGINE=InnoDB",
			append: "INSERT INTO " + t + " (k, v) VALUES (?, ?) ON DUPLICATE KEY UPDATE v = CONCAT(v, ',', VALUES(v))",
			read:   "SELECT v FROM " + t + " WHERE k = ?",
		}, nil
	}
	return dialect{}, fmt.Errorf("unsupported database %q: use postgres:// or mysql://", u.Scheme)
}

var levels = map[string]sql.IsolationLevel{
	"read-uncommitted": sql.LevelReadUncommitted,
	"read-committed":   sql.LevelReadCommitted,
	"repeatable-read":  sql.LevelRepeatableRead,
	"serializable":     sql.LevelSerializable,
}

// aborted reports an error after which the database is known to have rolled the transaction back.
func aborted(err error) bool {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code == "40001" || pe.Code == "40P01" || strings.HasPrefix(pe.Code, "23")
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return me.Number == 1213 || me.Number == 1205
	}
	return false
}

// keys hands out the keys transactions work on: a few at a time, each retired after MaxWrites
// appends so the lists stay short, and values unique across the whole run.
type keys struct {
	mu     sync.Mutex
	active []int
	writes map[int]int
	next   int
	value  int
	max    int
}

func (k *keys) mops(rng *rand.Rand, n int) []Mop {
	k.mu.Lock()
	defer k.mu.Unlock()
	var out []Mop
	for range n {
		i := rng.Intn(len(k.active))
		key := k.active[i]
		if rng.Intn(2) == 0 {
			out = append(out, Mop{F: Read, K: key})
			continue
		}
		k.value++
		out = append(out, Mop{F: Append, K: key, V: k.value})
		if k.writes[key]++; k.writes[key] >= k.max {
			k.active[i] = k.next
			k.next++
		}
	}
	return out
}

// Run sets up the table, runs the transactions and returns the history.
func Run(ctx context.Context, c Config, progress func(done int)) ([]Op, map[string]int, error) {
	d, err := dialectFor(c)
	if err != nil {
		return nil, nil, err
	}
	iso, ok := levels[c.Isolation]
	if !ok {
		return nil, nil, fmt.Errorf("unknown isolation level %q", c.Isolation)
	}
	db, err := sql.Open(d.driver, d.dsn)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(c.Clients + 1)
	db.SetMaxIdleConns(c.Clients + 1)
	for _, q := range []string{d.drop, d.create} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", q, err)
		}
	}

	ks := &keys{writes: map[int]int{}, max: c.MaxWrites}
	for i := range c.Keys {
		ks.active = append(ks.active, i)
	}
	ks.next = c.Keys

	start := time.Now()
	var mu sync.Mutex
	var ops []Op
	record := func(op Op) {
		mu.Lock()
		op.Index, op.Time = len(ops), time.Since(start).Nanoseconds()
		ops = append(ops, op)
		mu.Unlock()
	}
	var issued, done int
	errs := map[string]int{}
	var wg sync.WaitGroup
	for p := range c.Clients {
		wg.Add(1)
		go func(process int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(process)))
			for {
				mu.Lock()
				if issued >= c.Txns || ctx.Err() != nil {
					mu.Unlock()
					return
				}
				issued++
				mu.Unlock()
				mops := ks.mops(rng, 1+rng.Intn(c.MaxOps))
				record(Op{Type: "invoke", Process: process, Value: mops})
				status, result, err := runTxn(ctx, db, d, iso, mops)
				record(Op{Type: status, Process: process, Value: result})
				if err != nil {
					mu.Lock()
					errs[err.Error()]++
					mu.Unlock()
				}
				if status == "info" {
					process += c.Clients // its outcome is unknown, so it never reuses the id
				}
				mu.Lock()
				done++
				n := done
				mu.Unlock()
				if progress != nil {
					progress(n)
				}
			}
		}(p)
	}
	wg.Wait()
	return ops, errs, nil
}

func runTxn(ctx context.Context, db *sql.DB, d dialect, iso sql.IsolationLevel, mops []Mop) (string, []Mop, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: iso})
	if err != nil {
		return "fail", mops, err
	}
	result := make([]Mop, len(mops))
	for i, m := range mops {
		result[i] = m
		if m.F == Append {
			_, err = tx.ExecContext(ctx, d.append, m.K, strconv.Itoa(m.V))
		} else {
			var v sql.NullString
			err = tx.QueryRowContext(ctx, d.read, m.K).Scan(&v)
			if errors.Is(err, sql.ErrNoRows) {
				err = nil
			}
			result[i].List, err = parseList(v.String, err)
		}
		if err != nil {
			tx.Rollback()
			return "fail", mops, err // never committed
		}
	}
	if err := tx.Commit(); err != nil {
		if aborted(err) {
			return "fail", mops, err
		}
		return "info", mops, err // the commit may or may not have happened
	}
	return "ok", result, nil
}

func parseList(s string, err error) ([]int, error) {
	if err != nil {
		return nil, err
	}
	l := []int{}
	if s == "" {
		return l, nil
	}
	for _, x := range strings.Split(s, ",") {
		n, err := strconv.Atoi(x)
		if err != nil {
			return nil, fmt.Errorf("bad list %q", s)
		}
		l = append(l, n)
	}
	return l, nil
}
