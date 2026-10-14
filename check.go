package main

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Edge types between transactions, after Adya: ww (a later write to the same key), wr (a read of
// what the other wrote), rw (a read that missed the other's write) and rt (real time: the first
// finished before the second started).
type Edge uint8

const (
	WW Edge = 1 << iota
	WR
	RW
	RT
)

func (e Edge) String() string {
	var s []string
	for _, x := range []struct {
		e Edge
		n string
	}{{WW, "ww"}, {WR, "wr"}, {RW, "rw"}, {RT, "rt"}} {
		if e&x.e != 0 {
			s = append(s, x.n)
		}
	}
	return strings.Join(s, "+")
}

type Step struct {
	From, To int
	Type     Edge
	Why      string
}

type Anomaly struct {
	Kind  string
	Steps []Step // a cycle, for the cycle anomalies
	Txns  []int
	Why   string // for the others
}

type Result struct {
	Txns      []Txn
	OK        int
	Failed    int
	Unknown   int
	Anomalies map[string][]Anomaly
	Counts    map[string]int
}

// Which anomalies rule out which consistency model, weakest model first.
var Models = []struct {
	Name   string
	Forbid []string
}{
	{"read-committed", []string{"G0", "G1a", "G1b", "G1c", "internal", "garbage-read", "duplicate-elements", "incompatible-order"}},
	{"snapshot-isolation", []string{"G-single", "non-repeatable-read"}},
	{"repeatable-read", []string{"G-single", "G2-item", "non-repeatable-read"}},
	{"serializable", []string{"G-single", "G2-item"}},
	{"strict-serializable", []string{"G0-realtime", "G1c-realtime", "G-single-realtime", "G2-item-realtime"}},
}

// Violations lists the anomalies found that a model forbids; every model forbids what the
// weaker ones do, except that snapshot isolation allows G2-item.
func (r *Result) Violations(model string) []string {
	var forbid []string
	for _, m := range Models {
		if m.Name == "repeatable-read" && model == "snapshot-isolation" {
			continue
		}
		if m.Name == "snapshot-isolation" && model == "repeatable-read" {
			continue
		}
		forbid = append(forbid, m.Forbid...)
		if m.Name == model {
			break
		}
	}
	var out []string
	for _, k := range forbid {
		if r.Counts[k] > 0 && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

type graph struct {
	out  map[int]map[int]Edge
	why  map[[3]int]string
	keys []int
}

func (g *graph) add(a, b int, t Edge, why string) {
	if a == b {
		return
	}
	if g.out[a] == nil {
		g.out[a] = map[int]Edge{}
	}
	if g.out[a][b]&t == 0 {
		g.why[[3]int{a, b, int(t)}] = why
	}
	g.out[a][b] |= t
}

func listText(l []int) string {
	s := make([]string, len(l))
	for i, v := range l {
		s[i] = fmt.Sprint(v)
	}
	return "[" + strings.Join(s, " ") + "]"
}

// Check finds the anomalies in a list-append history.
func Check(txns []Txn) *Result {
	r := &Result{Txns: txns, Anomalies: map[string][]Anomaly{}, Counts: map[string]int{}}
	report := func(a Anomaly) {
		r.Counts[a.Kind]++
		if len(r.Anomalies[a.Kind]) < 5 {
			r.Anomalies[a.Kind] = append(r.Anomalies[a.Kind], a)
		}
	}
	type kv struct{ k, v int }
	writer := map[kv]int{}
	appends := map[int]map[int][]int{} // txn -> key -> values it appended, in order
	for i, t := range txns {
		switch t.Status {
		case "ok":
			r.OK++
		case "fail":
			r.Failed++
		default:
			r.Unknown++
		}
		for _, m := range t.Mops {
			if m.F != Append {
				continue
			}
			if w, dup := writer[kv{m.K, m.V}]; dup && w != i {
				report(Anomaly{Kind: "duplicate-appends", Txns: []int{w, i}, Why: fmt.Sprintf("value %d was appended to key %d twice", m.V, m.K)})
			}
			writer[kv{m.K, m.V}] = i
			if appends[i] == nil {
				appends[i] = map[int][]int{}
			}
			appends[i][m.K] = append(appends[i][m.K], m.V)
		}
	}

	// reads: values nobody wrote, values from aborted transactions, duplicates, and reads that
	// disagree with the transaction's own earlier reads and appends
	observed := map[int]bool{}
	longest := map[int][]int{}
	for i, t := range txns {
		if t.Status != "ok" {
			continue
		}
		last := map[int][]int{} // the key as the transaction last read it
		mine := map[int][]int{} // its appends to the key since then
		known := map[int]bool{}
		for _, m := range t.Mops {
			if m.F == Append {
				mine[m.K] = append(mine[m.K], m.V)
				continue
			}
			if m.List == nil {
				continue
			}
			seen := map[int]bool{}
			for _, v := range m.List {
				if seen[v] {
					report(Anomaly{Kind: "duplicate-elements", Txns: []int{i}, Why: fmt.Sprintf("read key %d = %s has %d twice", m.K, listText(m.List), v)})
				}
				seen[v] = true
				w, ok := writer[kv{m.K, v}]
				switch {
				case !ok:
					report(Anomaly{Kind: "garbage-read", Txns: []int{i}, Why: fmt.Sprintf("read %d in key %d, which nobody appended", v, m.K)})
				case txns[w].Status == "fail":
					report(Anomaly{Kind: "G1a", Txns: []int{w, i}, Why: fmt.Sprintf("T%d read %d in key %d, appended by T%d, which aborted", i, v, m.K, w)})
				default:
					observed[w] = true
				}
			}
			// a read must keep what the transaction read before and end with what it appended since;
			// anything new in between came from others, which only read committed allows
			prev, own := last[m.K], mine[m.K]
			switch {
			case !isPrefix(prev, m.List) || !hasSuffix(m.List, own):
				want := listText(append(slices.Clone(prev), own...))
				if !known[m.K] {
					want = "... " + listText(own)
				}
				report(Anomaly{Kind: "internal", Txns: []int{i}, Why: fmt.Sprintf("T%d read key %d = %s but its own reads and appends say %s", i, m.K, listText(m.List), want)})
			case known[m.K] && len(m.List) != len(prev)+len(own):
				report(Anomaly{Kind: "non-repeatable-read", Txns: []int{i}, Why: fmt.Sprintf("T%d read key %d as %s, then as %s with others' appends in between", i, m.K, listText(prev), listText(m.List))})
			}
			last[m.K], mine[m.K], known[m.K] = slices.Clone(m.List), nil, true
			if len(m.List) > len(longest[m.K]) {
				longest[m.K] = m.List
			}
		}
	}

	// each key's version order is its longest read; every other read must be a prefix of it
	bad := map[int]bool{}
	for i, t := range txns {
		if t.Status != "ok" {
			continue
		}
		for _, m := range t.Mops {
			if m.F == Read && m.List != nil && !isPrefix(m.List, longest[m.K]) && !bad[m.K] {
				bad[m.K] = true
				report(Anomaly{Kind: "incompatible-order", Txns: []int{i}, Why: fmt.Sprintf("key %d was read as %s and as %s", m.K, listText(m.List), listText(longest[m.K]))})
			}
		}
	}

	// G1b: a read that ends on a value its writer appended to the key before appending more
	for i, t := range txns {
		if t.Status != "ok" {
			continue
		}
		for _, m := range t.Mops {
			if m.F != Read || len(m.List) == 0 {
				continue
			}
			last := m.List[len(m.List)-1]
			w, ok := writer[kv{m.K, last}]
			if !ok || w == i {
				continue
			}
			vs := appends[w][m.K]
			if p := slices.Index(vs, last); p >= 0 && p < len(vs)-1 {
				report(Anomaly{Kind: "G1b", Txns: []int{w, i}, Why: fmt.Sprintf("T%d read key %d = %s, ending on %d, but T%d went on to append %d", i, m.K, listText(m.List), last, w, vs[p+1])})
			}
		}
	}

	// the transactions in the graph: committed ones, and unknown ones whose appends someone saw
	node := func(i int) bool { return txns[i].Status == "ok" || txns[i].Status == "info" && observed[i] }
	g := &graph{out: map[int]map[int]Edge{}, why: map[[3]int]string{}}
	keys := make([]int, 0, len(longest))
	for k := range longest {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	for _, k := range keys {
		if bad[k] {
			continue
		}
		order := longest[k]
		for j := 1; j < len(order); j++ {
			a, b := writer[kv{k, order[j-1]}], writer[kv{k, order[j]}]
			if node(a) && node(b) {
				g.add(a, b, WW, fmt.Sprintf("T%d appended %d to key %d, then T%d appended %d", a, order[j-1], k, b, order[j]))
			}
		}
	}
	for i, t := range txns {
		if t.Status != "ok" {
			continue
		}
		done := map[int]bool{}
		own := map[int][]int{}
		for _, m := range t.Mops {
			if m.F == Append {
				own[m.K] = append(own[m.K], m.V)
				continue
			}
			if done[m.K] || m.List == nil || bad[m.K] {
				continue
			}
			done[m.K] = true
			// what the transaction saw of others: the read without its own appends at the end
			ext := m.List
			if mine := own[m.K]; len(mine) > 0 {
				if !hasSuffix(ext, mine) {
					continue
				}
				ext = ext[:len(ext)-len(mine)]
			}
			order := longest[m.K]
			if n := len(ext); n > 0 {
				w := writer[kv{m.K, ext[n-1]}]
				if node(w) {
					g.add(w, i, WR, fmt.Sprintf("T%d read key %d = %s, seeing %d appended by T%d", i, m.K, listText(m.List), ext[n-1], w))
				}
			}
			if n := len(ext); n < len(order) {
				w := writer[kv{m.K, order[n]}]
				if node(w) && w != i {
					g.add(i, w, RW, fmt.Sprintf("T%d read key %d = %s, missing %d appended next by T%d", i, m.K, listText(m.List), order[n], w))
				}
			}
		}
	}

	// real time: an edge from every transaction that had finished when another started, kept to
	// the ones not implied by others (Elle's frontier)
	type ev struct {
		t     int64
		start bool
		i     int
	}
	var evs []ev
	for i, t := range txns {
		if !node(i) {
			continue
		}
		evs = append(evs, ev{t.Start, true, i})
		if t.Status == "ok" {
			evs = append(evs, ev{t.End, false, i})
		}
	}
	sort.Slice(evs, func(a, b int) bool {
		if evs[a].t != evs[b].t {
			return evs[a].t < evs[b].t
		}
		return evs[a].start && !evs[b].start // at the same instant neither finished before the other started
	})
	frontier := map[int]bool{}
	before := map[int][]int{}
	for _, e := range evs {
		if e.start {
			for f := range frontier {
				g.add(f, e.i, RT, fmt.Sprintf("T%d finished before T%d started", f, e.i))
				before[e.i] = append(before[e.i], f)
			}
			continue
		}
		for _, f := range before[e.i] {
			delete(frontier, f)
		}
		frontier[e.i] = true
	}

	for _, scc := range sccs(g) {
		findCycles(g, scc, report)
	}
	return r
}

func isPrefix(a, b []int) bool { return len(a) <= len(b) && slices.Equal(a, b[:len(a)]) }

func hasSuffix(a, b []int) bool { return len(b) <= len(a) && slices.Equal(a[len(a)-len(b):], b) }

// sccs returns the strongly connected components with more than one transaction (Tarjan,
// without recursion so long chains cannot overflow the stack).
func sccs(g *graph) [][]int { return sccsOf(g, nil, ^Edge(0)) }

// sccsOf works on the edges of the given types between the given transactions (all when nil).
func sccsOf(g *graph, in map[int]bool, mask Edge) [][]int {
	var nodes []int
	for a := range g.out {
		if in == nil || in[a] {
			nodes = append(nodes, a)
		}
	}
	sort.Ints(nodes)
	index, low := map[int]int{}, map[int]int{}
	on := map[int]bool{}
	var out [][]int
	var st []int
	n := 0
	succ := func(a int) []int {
		var s []int
		for b, t := range g.out[a] {
			if t&mask != 0 && (in == nil || in[b]) {
				s = append(s, b)
			}
		}
		sort.Ints(s)
		return s
	}
	for _, root := range nodes {
		if _, seen := index[root]; seen {
			continue
		}
		type frame struct {
			v    int
			next []int
		}
		frames := []frame{{root, succ(root)}}
		index[root], low[root] = n, n
		n++
		st = append(st, root)
		on[root] = true
		for len(frames) > 0 {
			f := &frames[len(frames)-1]
			if len(f.next) > 0 {
				w := f.next[0]
				f.next = f.next[1:]
				if _, seen := index[w]; !seen {
					index[w], low[w] = n, n
					n++
					st = append(st, w)
					on[w] = true
					frames = append(frames, frame{w, succ(w)})
				} else if on[w] && index[w] < low[f.v] {
					low[f.v] = index[w]
				}
				continue
			}
			v := f.v
			frames = frames[:len(frames)-1]
			if len(frames) > 0 && low[v] < low[frames[len(frames)-1].v] {
				low[frames[len(frames)-1].v] = low[v]
			}
			if low[v] == index[v] {
				var c []int
				for {
					w := st[len(st)-1]
					st = st[:len(st)-1]
					on[w] = false
					c = append(c, w)
					if w == v {
						break
					}
				}
				if len(c) > 1 {
					sort.Ints(c)
					out = append(out, c)
				}
			}
		}
	}
	return out
}

// The cycle searches: each starts from an edge of one type and looks for the shortest way back
// through the allowed types. A cycle is named by what it holds: no rw edge is G0 (only ww) or G1c,
// one rw edge is G-single, more is G2-item, and any real-time edge adds "-realtime". The
// real-time searches run only where the plain one found nothing, so what they find needs one.
var searches = []struct {
	kind  string
	start Edge
	path  Edge
}{
	{"G0", WW, WW},
	{"G1c", WR, WW | WR},
	{"G-single", RW, WW | WR},
	{"G2-item", RW, WW | WR | RW},
	{"G0-realtime", WW, WW | RT},
	{"G1c-realtime", WR, WW | WR | RT},
	{"G-single-realtime", RW, WW | WR | RT},
	{"G2-item-realtime", RW, WW | WR | RW | RT},
}

const maxStarts = 2000 // start edges tried per search in one component, when no shortcut applies

func classify(steps []Step) string {
	var rw, wr, rt int
	for _, s := range steps {
		switch s.Type {
		case RW:
			rw++
		case WR:
			wr++
		case RT:
			rt++
		}
	}
	k := "G0"
	switch {
	case rw == 1:
		k = "G-single"
	case rw > 1:
		k = "G2-item"
	case wr > 0:
		k = "G1c"
	}
	if rt > 0 {
		k += "-realtime"
	}
	return k
}

func findCycles(g *graph, scc []int, report func(Anomaly)) {
	in := map[int]bool{}
	for _, v := range scc {
		in[v] = true
	}
	found := map[string]bool{}
	for _, s := range searches {
		base := strings.TrimSuffix(s.kind, "-realtime")
		if found[s.kind] || base != s.kind && found[base] {
			continue
		}
		// when the start edge's type is allowed on the way back, a cycle through it exists exactly
		// when both ends sit in one component of the allowed edges
		comp := map[int]int{}
		if s.start&s.path != 0 {
			for c, members := range sccsOf(g, in, s.path) {
				for _, v := range members {
					comp[v] = c + 1
				}
			}
		}
		tries := 0
	search:
		for _, a := range scc {
			for _, b := range sortedKeys(g.out[a]) {
				if !in[b] || g.out[a][b]&s.start == 0 {
					continue
				}
				if s.start&s.path != 0 && (comp[a] == 0 || comp[a] != comp[b]) {
					continue
				}
				if tries++; tries > maxStarts {
					break search
				}
				path := shortest(g, in, b, a, s.path)
				if path == nil {
					continue
				}
				steps := append([]Step{{a, b, s.start, g.why[[3]int{a, b, int(s.start)}]}}, path...)
				// a cycle of another kind than the one sought is still reported, under its own name
				if k := classify(steps); !found[k] {
					report(Anomaly{Kind: k, Steps: steps})
					found[k] = true
					if k == s.kind {
						break search
					}
				}
			}
		}
	}
}

func usesRT(steps []Step) bool {
	for _, s := range steps {
		if s.Type == RT {
			return true
		}
	}
	return false
}

func sortedKeys(m map[int]Edge) []int {
	k := make([]int, 0, len(m))
	for x := range m {
		k = append(k, x)
	}
	sort.Ints(k)
	return k
}

// shortest finds the fewest steps from a to b inside the component through edges of the allowed
// types; each step is labelled with one of them, preferring the strongest dependency.
func shortest(g *graph, in map[int]bool, a, b int, allowed Edge) []Step {
	prev := map[int]int{a: -1}
	queue := []int{a}
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		if v == b {
			break
		}
		for _, w := range sortedKeys(g.out[v]) {
			if t := g.out[v][w]; !in[w] || t&allowed == 0 {
				continue
			}
			if _, seen := prev[w]; !seen {
				prev[w] = v
				queue = append(queue, w)
			}
		}
	}
	if _, ok := prev[b]; !ok || a == b {
		return nil
	}
	var steps []Step
	for w := b; w != a; w = prev[w] {
		v := prev[w]
		t := g.out[v][w] & allowed
		for _, x := range []Edge{WW, WR, RW, RT} {
			if t&x != 0 {
				t = x
				break
			}
		}
		steps = append(steps, Step{v, w, t, g.why[[3]int{v, w, int(t)}]})
	}
	slices.Reverse(steps)
	return steps
}
