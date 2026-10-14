package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
)

const usage = `isocheck runs concurrent transactions against a database and checks what isolation it gave.

  isocheck [flags] postgres://user@host:5432/db
  isocheck [flags] mysql://user:pass@host:3306/db
  isocheck check [-expect model] history.edn

It drops and creates the table named by -table (default isocheck).

Flags:
`

func main() {
	fs := flag.NewFlagSet("isocheck", flag.ExitOnError)
	isolation := fs.String("isolation", "serializable", "level to run at: read-uncommitted, read-committed, repeatable-read, serializable")
	expect := fs.String("expect", "", "model to hold the database to (default: the one -isolation promises): read-committed, snapshot-isolation, repeatable-read, serializable, strict-serializable")
	clients := fs.Int("clients", 10, "concurrent clients")
	txns := fs.Int("txns", 2000, "transactions to run")
	nkeys := fs.Int("keys", 8, "keys in play at once")
	maxWrites := fs.Int("max-writes", 32, "appends to a key before a fresh key replaces it")
	maxOps := fs.Int("max-ops", 4, "most reads and appends in one transaction")
	table := fs.String("table", "isocheck", "table to use; it is dropped first")
	save := fs.String("save", "", "write the history to this file, in the format Jepsen's Elle reads")
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage); fs.PrintDefaults() }

	args := os.Args[1:]
	checkOnly := len(args) > 0 && args[0] == "check"
	if checkOnly {
		args = args[1:]
	}
	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	model := *expect
	if model == "" {
		model = *isolation
		if model == "read-uncommitted" {
			model = ""
		}
	}

	var ops []Op
	if checkOnly {
		f, err := os.Open(fs.Arg(0))
		if err != nil {
			fail(err)
		}
		ops, err = ReadEDN(f)
		f.Close()
		if err != nil {
			fail(err)
		}
		if *expect == "" {
			model = "strict-serializable"
		}
	} else {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		cfg := Config{URL: fs.Arg(0), Isolation: *isolation, Clients: *clients, Txns: *txns, Keys: *nkeys, MaxWrites: *maxWrites, MaxOps: *maxOps, Table: *table}
		var errs map[string]int
		var err error
		ops, errs, err = Run(ctx, cfg, func(n int) {
			if n%500 == 0 || n == *txns {
				fmt.Fprintf(os.Stderr, "\r%d of %d transactions", n, *txns)
			}
		})
		fmt.Fprintln(os.Stderr)
		if err != nil {
			fail(err)
		}
		printErrors(errs)
		if *save != "" {
			f, err := os.Create(*save)
			if err != nil {
				fail(err)
			}
			if err := WriteEDN(f, ops); err != nil {
				fail(err)
			}
			f.Close()
		}
	}
	txs, err := Txns(ops)
	if err != nil {
		fail(err)
	}
	r := Check(txs)
	Print(os.Stdout, r)
	if model != "" {
		if v := r.Violations(model); len(v) > 0 {
			fmt.Printf("\nNot %s.\n", model)
			os.Exit(1)
		}
		fmt.Printf("\nConsistent with %s.\n", model)
	}
}

func printErrors(errs map[string]int) {
	type e struct {
		msg string
		n   int
	}
	var list []e
	for m, n := range errs {
		list = append(list, e{m, n})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].n > list[j].n })
	for i, x := range list {
		if i == 3 {
			break
		}
		fmt.Fprintf(os.Stderr, "%6d x %s\n", x.n, x.msg)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "isocheck:", err)
	os.Exit(2)
}
