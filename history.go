package main

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// A history is what the clients saw: each transaction's invocation and its completion.

const (
	Append = "append"
	Read   = "r"
)

type Mop struct {
	F    string // Append or Read
	K    int
	V    int   // the value appended
	List []int // what a read returned; nil before the read completes
}

type Txn struct {
	ID      int
	Process int
	Status  string // "ok", "fail" or "info" (unknown whether it committed)
	Start   int64  // when it was invoked
	End     int64  // when it completed
	Mops    []Mop
}

// Op is one line of a Jepsen history: an invocation or a completion.
type Op struct {
	Index   int
	Time    int64
	Type    string // invoke, ok, fail, info
	Process int
	Value   []Mop
}

// Txns pairs each invocation with the completion that follows it on the same process.
func Txns(ops []Op) ([]Txn, error) {
	open := map[int]int{}
	var txns []Txn
	for _, op := range ops {
		switch op.Type {
		case "invoke":
			if _, ok := open[op.Process]; ok {
				return nil, fmt.Errorf("process %d invoked twice without completing", op.Process)
			}
			open[op.Process] = len(txns)
			txns = append(txns, Txn{ID: len(txns), Process: op.Process, Start: op.Time, Status: "info", Mops: op.Value})
		case "ok", "fail", "info":
			i, ok := open[op.Process]
			if !ok {
				return nil, fmt.Errorf("process %d completed without an invocation", op.Process)
			}
			delete(open, op.Process)
			txns[i].Status, txns[i].End = op.Type, op.Time
			if op.Type == "ok" {
				txns[i].Mops = op.Value
			}
		default:
			return nil, fmt.Errorf("unknown op type %q", op.Type)
		}
	}
	for _, i := range open {
		txns[i].End = 1<<63 - 1 // never completed
	}
	return txns, nil
}

// WriteEDN writes a history in the format Jepsen's Elle reads.
func WriteEDN(w io.Writer, ops []Op) error {
	bw := bufio.NewWriter(w)
	for _, op := range ops {
		fmt.Fprintf(bw, "{:index %d, :time %d, :type :%s, :process %d, :value [", op.Index, op.Time, op.Type, op.Process)
		for i, m := range op.Value {
			if i > 0 {
				bw.WriteByte(' ')
			}
			switch {
			case m.F == Append:
				fmt.Fprintf(bw, "[:append %d %d]", m.K, m.V)
			case m.List == nil:
				fmt.Fprintf(bw, "[:r %d nil]", m.K)
			default:
				fmt.Fprintf(bw, "[:r %d [", m.K)
				for j, v := range m.List {
					if j > 0 {
						bw.WriteByte(' ')
					}
					bw.WriteString(strconv.Itoa(v))
				}
				bw.WriteString("]]")
			}
		}
		bw.WriteString("]}\n")
	}
	return bw.Flush()
}

// ReadEDN reads a list-append history: one map per line, as Jepsen writes them.
func ReadEDN(r io.Reader) ([]Op, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	var ops []Op
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		p := &ednParser{s: text}
		v, err := p.value()
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("line %d: expected a map", line)
		}
		op := Op{Index: len(ops), Time: int64(len(ops))}
		if x, ok := m["index"].(int); ok {
			op.Index = x
		}
		if x, ok := m["time"].(int); ok {
			op.Time = int64(x)
		}
		op.Type, _ = m["type"].(string)
		op.Process, _ = m["process"].(int)
		vals, _ := m["value"].([]any)
		for _, x := range vals {
			mv, ok := x.([]any)
			if !ok || len(mv) != 3 {
				return nil, fmt.Errorf("line %d: bad micro-op %v", line, x)
			}
			f, _ := mv[0].(string)
			k, ok := mv[1].(int)
			if !ok {
				return nil, fmt.Errorf("line %d: key %v is not an integer", line, mv[1])
			}
			mop := Mop{F: f, K: k}
			switch f {
			case Append:
				v, ok := mv[2].(int)
				if !ok {
					return nil, fmt.Errorf("line %d: appended value %v is not an integer", line, mv[2])
				}
				mop.V = v
			case Read:
				if l, ok := mv[2].([]any); ok {
					mop.List = []int{}
					for _, e := range l {
						n, ok := e.(int)
						if !ok {
							return nil, fmt.Errorf("line %d: read element %v is not an integer", line, e)
						}
						mop.List = append(mop.List, n)
					}
				}
			default:
				return nil, fmt.Errorf("line %d: unknown micro-op %q", line, f)
			}
			op.Value = append(op.Value, mop)
		}
		ops = append(ops, op)
	}
	sort.SliceStable(ops, func(i, j int) bool { return ops[i].Index < ops[j].Index })
	return ops, sc.Err()
}

// ednParser reads the part of EDN a history uses: maps, vectors, lists, keywords, integers,
// strings, nil and booleans.
type ednParser struct {
	s string
	i int
}

func (p *ednParser) skip() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == ',' || p.s[p.i] == '\t' || p.s[p.i] == '\n' || p.s[p.i] == '\r') {
		p.i++
	}
}

func (p *ednParser) value() (any, error) {
	p.skip()
	if p.i >= len(p.s) {
		return nil, fmt.Errorf("unexpected end")
	}
	switch c := p.s[p.i]; {
	case c == '{':
		p.i++
		m := map[string]any{}
		for {
			p.skip()
			if p.i < len(p.s) && p.s[p.i] == '}' {
				p.i++
				return m, nil
			}
			k, err := p.value()
			if err != nil {
				return nil, err
			}
			v, err := p.value()
			if err != nil {
				return nil, err
			}
			ks, _ := k.(string)
			m[ks] = v
		}
	case c == '[' || c == '(':
		end := byte(']')
		if c == '(' {
			end = ')'
		}
		p.i++
		var l []any
		for {
			p.skip()
			if p.i < len(p.s) && p.s[p.i] == end {
				p.i++
				if l == nil {
					l = []any{}
				}
				return l, nil
			}
			v, err := p.value()
			if err != nil {
				return nil, err
			}
			l = append(l, v)
		}
	case c == ':':
		p.i++
		return p.token(), nil
	case c == '"':
		j := strings.IndexByte(p.s[p.i+1:], '"')
		if j < 0 {
			return nil, fmt.Errorf("unterminated string")
		}
		s := p.s[p.i+1 : p.i+1+j]
		p.i += j + 2
		return s, nil
	default:
		t := p.token()
		switch t {
		case "nil":
			return nil, nil
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
		n, err := strconv.Atoi(t)
		if err != nil {
			return nil, fmt.Errorf("unexpected %q", t)
		}
		return n, nil
	}
}

func (p *ednParser) token() string {
	j := p.i
	for p.i < len(p.s) && !strings.ContainsRune(" ,\t\n\r[](){}", rune(p.s[p.i])) {
		p.i++
	}
	return p.s[j:p.i]
}
