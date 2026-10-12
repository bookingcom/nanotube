package rules

import (
	"bytes"
	"math/rand"
	"os"
	"regexp"
	"strings"
	"testing"
)

// Regexps shaped like production routing rules plus shapes that exercise every
// branch of the matcher.
var matcherCorpusRegexs = []string{
	`^carbon.probe.(availability|latency.cache).secondly.containers.`,
	`^carbon.probe.(availability|latency.cache).secondly.aggr.`,
	`^aggregations\.(minutely|hourly)\.capacity\.platform\.secondly\..+\.pods\.per_service\.`,
	`^aggregations\.secondly\..+\.by_service_cluster_pod\.`,
	`^aggregations\.secondly\.platform\.[^.]+\.per_env\.[^.]+\.per_cluster\.`,
	`^loadbalancers\.group\..*\.kube-probe-`,
	`^general\.search\..+\.queryTopic`,
	`^user\.[^\.]+\.authserver\.`,
	`^carbon\.agents\.(.+)`,
	`^.+`,
	`^sys\.(.+)_([A-Za-z0-9-]+)_([A-Za-z0-9]+)_example_com\.(.+)`,
	`abc.*`, `cbd.*`, `ab[xyz]test\.y{5}\.a?\.somethingx+\.test`, `xyz.*xyz`,
	`^a.*`, `^abc.*`, `^abcabcabcabc.*`,
	``, `^`, `$`, `^$`, `^a$`, `a`, `^a`, `a$`, `^.*$`, `^.+$`, `.+`, `.*`,
	`^a.+b$`, `^a.*b$`, `a.+b`, `a.*b.*c`, `^a.+b.+c$`, `.+a`, `a.+`, `^.+a.+$`,
	`^(abc)(def)`, `^(abc)+def`, `(?i)abc`, `^(?i)abc.+d`, `(?s)a.+b`, `(?s)^a.*b$`,
	`a.*?b`, `^a.+?b`, `.+.+`, `^a..b`, `^a.b`, `a.{2,}b`, `a.{1,}b`, `a.{0,}b`,
	`^é.+x`, `^a.+é`, `\x{FFFD}`, `^a.+\x{FFFD}`, `a\nb`, `^a.+\n`, `(?m)^abc`,
	`^aa.+aa$`, `^aba.+aba$`, `^ab.*ba$`, `a()b`, `^(|a)b`, `x|y`, `^(x|y).+z`,
}

const matcherAlphabet = "ab.c_-:xyzé\n\xff\xe2\x82AB"

func randomInput(rnd *rand.Rand) []byte {
	n := rnd.Intn(16)
	b := make([]byte, 0, n)
	for i := 0; i < n; i++ {
		b = append(b, matcherAlphabet[rnd.Intn(len(matcherAlphabet))])
	}
	return b
}

var matcherPieces = []string{
	"a", "b", "ab", "aa", ".", `\.`, ".*", ".+", ".*?", ".+?", "(a|b)", "[^.]+", "x",
	"(", ")", "é", "_", "^", "$", "(?i)", "(?s:.)+", "a?", "b+", "\n",
}

func randomRegex(rnd *rand.Rand) string {
	var sb strings.Builder
	if rnd.Intn(2) == 0 {
		sb.WriteString("^")
	}
	for i := rnd.Intn(6); i > 0; i-- {
		sb.WriteString(matcherPieces[rnd.Intn(len(matcherPieces))])
	}
	if rnd.Intn(3) == 0 {
		sb.WriteString("$")
	}
	return sb.String()
}

func checkMatcher(t *testing.T, re *regexp.Regexp, m *matcher, in []byte) {
	t.Helper()
	if got, want := m.Match(in), re.Match(in); got != want {
		t.Fatalf("regex %q input %q: fast=%v regexp=%v (glob=%v prefix=%q required=%q)",
			re.String(), in, got, want, m.glob, m.prefix, m.required)
	}
}

func TestMatcherDifferential(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))

	exprs := append([]string{}, matcherCorpusRegexs...)
	for i := 0; i < 3000; i++ {
		exprs = append(exprs, randomRegex(rnd))
	}

	inputs := [][]byte{
		nil, []byte(""), []byte("a"), []byte("ab"), []byte("\n"), []byte("a\nb"),
		[]byte("aggregations.secondly.foo.by_service_cluster_pod.x"),
		[]byte("aggregations.secondly..by_service_cluster_pod."),
		[]byte("aggregations.secondly.by_service_cluster_pod.x"),
		[]byte("aggregations.secondly.platform.a.per_env.b.per_cluster.c"),
		[]byte("aggregations.minutely.capacity.platform.secondly.x.pods.per_service.y"),
		[]byte("carbon.probe.availability.secondly.containers.x"),
		[]byte("carbon.agents.host.cache.size"),
		[]byte("éa\xffb"), []byte("a\xe2\x82b"), []byte("aba"), []byte("abba"),
	}
	for i := 0; i < 400; i++ {
		inputs = append(inputs, randomInput(rnd))
	}

	compiled := 0
	for _, expr := range exprs {
		re, err := regexp.Compile(expr)
		if err != nil {
			continue
		}
		compiled++
		m := newMatcher(re)
		for _, in := range inputs {
			checkMatcher(t, re, &m, in)
		}
	}
	if compiled < 1000 {
		t.Fatalf("only %d regexps compiled; generator is broken", compiled)
	}
}

func TestMatcherStrategies(t *testing.T) {
	tests := []struct {
		expr     string
		glob     bool
		prefix   string
		required []string
	}{
		{expr: `^aggregations\.secondly\..+\.by_service_cluster_pod\.`, glob: true},
		{expr: `^.+`, glob: true},
		{expr: `abc.*`, glob: true},
		{expr: `^carbon.probe.(availability|latency.cache).secondly.containers.`,
			prefix: "carbon", required: []string{"carbon", "probe", "secondly", "containers"}},
		{expr: `^aggregations\.secondly\.platform\.[^.]+\.per_env\.[^.]+\.per_cluster\.`,
			prefix: "aggregations.secondly.platform.", required: []string{"aggregations.secondly.platform.", ".per_env.", ".per_cluster."}},
		{expr: `(?i)abc`},
	}

	for _, tt := range tests {
		m := newMatcher(regexp.MustCompile(tt.expr))
		if (m.glob != nil) != tt.glob {
			t.Errorf("%q: glob=%v, want %v", tt.expr, m.glob != nil, tt.glob)
		}
		if string(m.prefix) != tt.prefix {
			t.Errorf("%q: prefix=%q, want %q", tt.expr, m.prefix, tt.prefix)
		}
		var req []string
		for _, r := range m.required {
			req = append(req, string(r))
		}
		if strings.Join(req, "|") != strings.Join(tt.required, "|") {
			t.Errorf("%q: required=%q, want %q", tt.expr, req, tt.required)
		}
	}
}

func FuzzMatcher(f *testing.F) {
	for _, e := range matcherCorpusRegexs {
		f.Add(e, "aggregations.secondly.foo.by_service_cluster_pod.x")
	}
	f.Fuzz(func(t *testing.T, expr string, in string) {
		re, err := regexp.Compile(expr)
		if err != nil {
			return
		}
		m := newMatcher(re)
		checkMatcher(t, re, &m, []byte(in))
	})
}

func BenchmarkMatcherProdRules(b *testing.B) {
	paths := [][]byte{
		[]byte("aggregations.secondly.auth.token_in_header.by_role_type.default.by_service.reports-receiver.by_ua_type.bot.by_host.example_com.present.count"),
		[]byte("aggregations.secondly.platform.app.by_service_cluster_pod.b-foo.k8s-cluster-a.pod-1234567-abcde.requests.count"),
	}
	exprs := matcherCorpusRegexs[:5]
	var res []*regexp.Regexp
	var ms []matcher
	for _, e := range exprs {
		re := regexp.MustCompile(e)
		res = append(res, re)
		ms = append(ms, newMatcher(re))
	}

	b.Run("regexp", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			for _, p := range paths {
				for _, re := range res {
					re.Match(p)
				}
			}
		}
	})
	b.Run("matcher", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			for _, p := range paths {
				for j := range ms {
					ms[j].Match(p)
				}
			}
		}
	})
}

// TestMatcherCapture checks the matcher against regexp on a captured traffic
// file (one record per line), e.g. NANOTUBE_CAPTURE=/path/to/in.txt.
func TestMatcherCapture(t *testing.T) {
	path := os.Getenv("NANOTUBE_CAPTURE")
	if path == "" {
		t.Skip("NANOTUBE_CAPTURE not set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var res []*regexp.Regexp
	var ms []matcher
	for _, e := range matcherCorpusRegexs {
		re, err := regexp.Compile(e)
		if err != nil {
			continue
		}
		res = append(res, re)
		ms = append(ms, newMatcher(re))
	}

	n := 0
	for len(data) > 0 {
		line := data
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			line, data = data[:i], data[i+1:]
		} else {
			data = nil
		}
		p := line
		if i := bytes.IndexByte(line, ' '); i >= 0 {
			p = line[:i]
		}
		for i := range ms {
			checkMatcher(t, res[i], &ms[i], p)
			checkMatcher(t, res[i], &ms[i], line)
		}
		n++
	}
	t.Logf("checked %d records against %d regexps", n, len(ms))
}
