package main

import (
	"slices"
	"testing"
)

func ap(k, v int) Mop          { return Mop{F: Append, K: k, V: v} }
func rd(k int, l ...int) Mop    { return Mop{F: Read, K: k, List: append([]int{}, l...)} }
func tx(status string, start, end int64, mops ...Mop) Txn {
	return Txn{Status: status, Start: start, End: end, Mops: mops}
}

func run(txns ...Txn) *Result {
	for i := range txns {
		txns[i].ID = i
	}
	return Check(txns)
}

func kinds(r *Result) []string {
	var k []string
	for name, n := range r.Counts {
		if n > 0 {
			k = append(k, name)
		}
	}
	slices.Sort(k)
	return k
}

func TestAnomalies(t *testing.T) {
	cases := []struct {
		name string
		txns []Txn
		want []string
	}{
		{"serial", []Txn{
			tx("ok", 0, 1, ap(1, 1)),
			tx("ok", 2, 3, rd(1, 1), ap(1, 2)),
			tx("ok", 4, 5, rd(1, 1, 2)),
		}, nil},
		{"G0: two keys written in opposite orders", []Txn{
			tx("ok", 0, 10, ap(1, 1), ap(2, 2)),
			tx("ok", 0, 10, ap(1, 3), ap(2, 4)),
			tx("ok", 20, 30, rd(1, 1, 3), rd(2, 4, 2)),
		}, []string{"G0"}},
		{"G1a: read of an aborted append", []Txn{
			tx("fail", 0, 1, ap(1, 1)),
			tx("ok", 2, 3, rd(1, 1)),
		}, []string{"G1a"}},
		{"G1b: read of an intermediate state", []Txn{
			tx("ok", 0, 10, ap(1, 1), ap(1, 2)),
			tx("ok", 0, 10, rd(1, 1)),
		}, []string{"G1b"}},
		{"G1c: each read the other's write", []Txn{
			tx("ok", 0, 10, ap(1, 1), rd(2, 2)),
			tx("ok", 0, 10, ap(2, 2), rd(1, 1)),
		}, []string{"G1c"}},
		{"G-single: saw one of two appends", []Txn{
			tx("ok", 0, 10, ap(1, 1), ap(2, 2)),
			tx("ok", 0, 10, rd(1), rd(2, 2)),
			tx("ok", 20, 30, rd(1, 1)),
		}, []string{"G-single"}},
		{"G2-item: write skew", []Txn{
			tx("ok", 0, 10, rd(1), ap(2, 2)),
			tx("ok", 0, 10, rd(2), ap(1, 1)),
			tx("ok", 20, 30, rd(1, 1), rd(2, 2)),
		}, []string{"G2-item"}},
		{"G-single-realtime: a stale read after the write finished", []Txn{
			tx("ok", 0, 10, ap(1, 1)),
			tx("ok", 20, 30, rd(1)),
			tx("ok", 40, 50, rd(1, 1)),
		}, []string{"G-single-realtime"}},
		{"concurrent stale read is fine", []Txn{
			tx("ok", 0, 10, ap(1, 1)),
			tx("ok", 5, 30, rd(1)),
			tx("ok", 40, 50, rd(1, 1)),
		}, nil},
		{"incompatible order", []Txn{
			tx("ok", 0, 1, ap(1, 1)),
			tx("ok", 0, 1, ap(1, 2)),
			tx("ok", 2, 3, rd(1, 1, 2)),
			tx("ok", 2, 3, rd(1, 2, 1)),
		}, []string{"incompatible-order"}},
		{"internal: a read that misses its own append", []Txn{
			tx("ok", 0, 1, ap(1, 5), rd(1)),
		}, []string{"internal"}},
		{"garbage read", []Txn{
			tx("ok", 0, 1, rd(1, 99)),
		}, []string{"garbage-read"}},
		{"duplicate elements", []Txn{
			tx("ok", 0, 1, ap(1, 1)),
			tx("ok", 2, 3, rd(1, 1, 1)),
		}, []string{"duplicate-elements"}},
		{"an unknown transaction someone saw counts as committed", []Txn{
			tx("info", 0, 1, ap(1, 1)),
			tx("ok", 2, 3, rd(1, 1)),
		}, nil},
		{"own appends before a read are not dependencies", []Txn{
			tx("ok", 0, 1, ap(1, 1)),
			tx("ok", 2, 3, ap(1, 2), rd(1, 1, 2)),
			tx("ok", 4, 5, rd(1, 1, 2)),
		}, nil},
	}
	for _, c := range cases {
		r := run(c.txns...)
		if got := kinds(r); !slices.Equal(got, c.want) {
			t.Errorf("%s: found %v, want %v", c.name, got, c.want)
			for _, list := range r.Anomalies {
				for _, a := range list {
					t.Logf("  %s %s %v", a.Kind, a.Why, a.Steps)
				}
			}
		}
	}
}

func TestModels(t *testing.T) {
	skew := run(
		tx("ok", 0, 10, rd(1), ap(2, 2)),
		tx("ok", 0, 10, rd(2), ap(1, 1)),
		tx("ok", 20, 30, rd(1, 1), rd(2, 2)),
	)
	for model, ok := range map[string]bool{"read-committed": true, "snapshot-isolation": true, "repeatable-read": false, "serializable": false, "strict-serializable": false} {
		if got := len(skew.Violations(model)) == 0; got != ok {
			t.Errorf("write skew under %s: consistent = %v, want %v", model, got, ok)
		}
	}
	stale := run(
		tx("ok", 0, 10, ap(1, 1)),
		tx("ok", 20, 30, rd(1)),
		tx("ok", 40, 50, rd(1, 1)),
	)
	if len(stale.Violations("serializable")) != 0 || len(stale.Violations("strict-serializable")) == 0 {
		t.Errorf("a stale read should be serializable but not strict serializable: %v %v", stale.Violations("serializable"), stale.Violations("strict-serializable"))
	}
}
