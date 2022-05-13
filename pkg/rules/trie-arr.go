package rules

// PrefixTrieArr efficiently checks if a slice of bytes contains one of the prefixes prevoiusly
// added to the tree.
type PrefixTrieArr struct {
	nxt [256]*PrefixTrieArr
	end bool
}

// NewPrefixTrieArr makes a new instance of a PrefixTrieArr.
func NewPrefixTrieArr() *PrefixTrieArr {
	return &PrefixTrieArr{}
}

// Add a prefix to the trie
func (t *PrefixTrieArr) Add(s []byte) {
	n := t
	for _, c := range s {
		if n.nxt[c] == nil {
			n.nxt[c] = &PrefixTrieArr{}
		}
		n = n.nxt[c]
	}

	n.end = true
}

// Check if string s contains any of the prefixes
func (t *PrefixTrieArr) Check(s []byte) bool {
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
