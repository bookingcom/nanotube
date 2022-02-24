package rules

// PrefixTrie efficiently checks if a slice of bytes contains one of the prefixes prevoiusly
// added to the tree.
type PrefixTrie struct {
	nxt map[byte]*PrefixTrie
	end bool
}

// NewPrefixTrie makes a new instance of a PrefixTrie.
func NewPrefixTrie() *PrefixTrie {
	return &PrefixTrie{
		nxt: make(map[byte]*PrefixTrie),
	}
}

// Add a prefix to the trie
func (t *PrefixTrie) Add(s []byte) {
	n := t
	for _, c := range s {
		if n.nxt[c] == nil {
			n.nxt[c] = &PrefixTrie{
				nxt: make(map[byte]*PrefixTrie),
			}
		}
		n = n.nxt[c]
	}

	n.end = true
}

// Check if string s contains any of the prefixes
func (t *PrefixTrie) Check(s []byte) bool {
	if len(s) == 0 {
		return true
	}

	n := t

	for _, c := range s {
		if n.nxt[c] == nil {
			return false
		}
		n = n.nxt[c]
		if n.end {
			return true
		}
	}

	return false
}
