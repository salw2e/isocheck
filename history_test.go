package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestEDNRoundTrip(t *testing.T) {
	ops := []Op{
		{Index: 0, Time: 10, Type: "invoke", Process: 0, Value: []Mop{{F: Append, K: 1, V: 7}, {F: Read, K: 2}}},
		{Index: 1, Time: 12, Type: "invoke", Process: 1, Value: []Mop{{F: Read, K: 1}}},
		{Index: 2, Time: 20, Type: "ok", Process: 0, Value: []Mop{{F: Append, K: 1, V: 7}, {F: Read, K: 2, List: []int{}}}},
		{Index: 3, Time: 25, Type: "fail", Process: 1, Value: []Mop{{F: Read, K: 1}}},
	}
	var b bytes.Buffer
	if err := WriteEDN(&b, ops); err != nil {
		t.Fatal(err)
	}
	got, err := ReadEDN(&b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, ops) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, ops)
	}
}

// a history as Jepsen writes it, with extra keys and a list for the value
func TestReadJepsenHistory(t *testing.T) {
	h := `{:index 0 :time 5 :type :invoke :process 3 :f :txn :value [[:append 9 1] [:r 9 nil]]}
{:index 1 :time 9 :type :ok :process 3 :f :txn :value [[:append 9 1] [:r 9 [1]]]}`
	ops, err := ReadEDN(strings.NewReader(h))
	if err != nil {
		t.Fatal(err)
	}
	txns, err := Txns(ops)
	if err != nil || len(txns) != 1 || txns[0].Status != "ok" || !reflect.DeepEqual(txns[0].Mops[1].List, []int{1}) {
		t.Fatalf("got %+v %v", txns, err)
	}
}
