package secretdetect

// A trie finds rule keywords in one pass over a text, betterleaks' keyword
// filter without hundreds of scans.
type trie struct {
	words []string
	index map[string]int
	root  [256]int32 // nodes by first byte; 0 for none
	nodes []node     // nodes[0] is unused
}

type node struct {
	word int // the keyword ending here, or -1
	next []edge
}

type edge struct {
	c byte
	n int32
}

// add adds keyword k and returns its index.
func (t *trie) add(k string) int {
	if i, ok := t.index[k]; ok {
		return i
	}
	if t.index == nil {
		t.index = map[string]int{}
		t.nodes = []node{{word: -1}}
	}
	i := len(t.words)
	t.words = append(t.words, k)
	t.index[k] = i
	if k == "" {
		return i
	}
	n := &t.root[k[0]]
	for j := 0; ; j++ {
		if *n == 0 {
			*n = int32(len(t.nodes))
			t.nodes = append(t.nodes, node{word: -1})
		}
		if j == len(k)-1 {
			t.nodes[*n].word = i
			return i
		}
		cur := &t.nodes[*n]
		n = nil
		for e := range cur.next {
			if cur.next[e].c == k[j+1] {
				n = &cur.next[e].n
				break
			}
		}
		if n == nil {
			cur.next = append(cur.next, edge{c: k[j+1]})
			n = &cur.next[len(cur.next)-1].n
		}
	}
}

// mark calls f with each keyword in lower, which is lowercase, as often
// as it's there.
func (t *trie) mark(lower string, f func(keyword int)) {
	for i := 0; i < len(lower); i++ {
		for n, j := t.root[lower[i]], i+1; n != 0; j++ {
			if w := t.nodes[n].word; w >= 0 {
				f(w)
			}
			if j == len(lower) {
				break
			}
			n = t.nodes[n].child(lower[j])
		}
	}
}

func (n *node) child(c byte) int32 {
	for _, e := range n.next {
		if e.c == c {
			return e.n
		}
	}
	return 0
}
