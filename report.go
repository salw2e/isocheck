package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

var cycleKinds = []string{"G0", "G1c", "G-single", "G2-item", "G0-realtime", "G1c-realtime", "G-single-realtime", "G2-item-realtime"}

var explain = map[string]string{
	"G0":                  "write cycle: two transactions' writes interleaved",
	"G1a":                 "aborted read: saw a write from a transaction that rolled back",
	"G1b":                 "intermediate read: saw a transaction's write before its later writes to the same key",
	"G1c":                 "circular information flow: transactions saw each other's writes",
	"G-single":            "read skew: a transaction saw some of another's writes but not all",
	"G2-item":             "write skew: transactions each missed the other's write",
	"G0-realtime":         "write cycle that needs real time order",
	"G1c-realtime":        "circular information flow that needs real time order",
	"G-single-realtime":   "stale read: missed a write that had already committed when it started",
	"G2-item-realtime":    "write skew that needs real time order",
	"internal":            "a transaction's read disagreed with its own earlier reads and writes",
	"non-repeatable-read": "a transaction read a key twice and saw others' new writes the second time",
	"garbage-read":        "read a value nobody wrote",
	"duplicate-elements":  "read the same appended value twice",
	"incompatible-order":  "two reads of a key disagree on the order of its values",
	"duplicate-appends":   "the history appended the same value twice (a broken workload)",
}

func mopsText(ms []Mop) string {
	var s []string
	for _, m := range ms {
		if m.F == Append {
			s = append(s, fmt.Sprintf("append %d %d", m.K, m.V))
		} else if m.List == nil {
			s = append(s, fmt.Sprintf("r %d", m.K))
		} else {
			s = append(s, fmt.Sprintf("r %d %s", m.K, listText(m.List)))
		}
	}
	return strings.Join(s, ", ")
}

func Print(w io.Writer, r *Result) {
	fmt.Fprintf(w, "%d transactions: %d committed, %d aborted, %d unknown\n", len(r.Txns), r.OK, r.Failed, r.Unknown)
	var kinds []string
	for k, n := range r.Counts {
		if n > 0 {
			kinds = append(kinds, k)
		}
	}
	order := func(k string) int {
		for i, c := range append(cycleKinds, "G1a", "G1b", "internal", "non-repeatable-read", "garbage-read", "duplicate-elements", "incompatible-order", "duplicate-appends") {
			if c == k {
				return i
			}
		}
		return 99
	}
	sort.Slice(kinds, func(i, j int) bool { return order(kinds[i]) < order(kinds[j]) })
	for _, k := range kinds {
		a := r.Anomalies[k][0]
		fmt.Fprintf(w, "\n%s, found %d times: %s\n", k, r.Counts[k], explain[k])
		if len(a.Steps) == 0 {
			fmt.Fprintf(w, "  %s\n", a.Why)
			for _, t := range a.Txns {
				fmt.Fprintf(w, "  T%d: %s\n", t, mopsText(r.Txns[t].Mops))
			}
			continue
		}
		for _, s := range a.Steps {
			fmt.Fprintf(w, "  %-2s %s\n", s.Type, s.Why)
		}
		seen := map[int]bool{}
		for _, s := range a.Steps {
			if !seen[s.From] {
				seen[s.From] = true
				fmt.Fprintf(w, "  T%d: %s\n", s.From, mopsText(r.Txns[s.From].Mops))
			}
		}
	}
	fmt.Fprintln(w)
	for _, m := range Models {
		v := r.Violations(m.Name)
		if len(v) == 0 {
			fmt.Fprintf(w, "%-20s yes\n", m.Name)
		} else {
			fmt.Fprintf(w, "%-20s no   %s\n", m.Name, strings.Join(v, ", "))
		}
	}
}
