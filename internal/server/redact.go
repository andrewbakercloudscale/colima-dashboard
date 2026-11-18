package server

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// Redactor scrubs a support bundle before it leaves the Mac: the home folder becomes ~, the
// macOS user name and the session token are replaced, and anything that looks like a secret
// (environment values and settings whose name says KEY, TOKEN, SECRET, PASSWORD and the like,
// Docker registry auths, kubeconfig client keys and certificates, private keys, bearer tokens)
// is replaced with <redacted>.
type Redactor struct {
	home, token string
	user        *regexp.Regexp
}

// NewRedactor returns a Redactor for a home folder (e.g. /Users/name), the macOS user name and
// the dashboard's session token. Empty values are skipped.
func NewRedactor(home, user, token string) Redactor {
	r := Redactor{home: strings.TrimSuffix(home, "/"), token: token}
	if len(user) >= 3 {
		r.user = regexp.MustCompile(`\b` + regexp.QuoteMeta(user) + `\b`)
	}
	return r
}

const redacted = "<redacted>"

// secretName matches setting and variable names whose values are secrets.
var secretName = regexp.MustCompile(`(?i)(key|token|secret|passw|credential|private|certificate-data|cookie|session|^auths?$|apikey|bearer)`)

var (
	// PEM private keys, whole blocks.
	pemKey = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	// "name": "value" pairs inside JSON text (logs written as JSON lines).
	jsonPair = regexp.MustCompile(`"([A-Za-z0-9_.\-]+)"(\s*:\s*)"((?:[^"\\]|\\.)*)"`)
	// NAME=value anywhere in a line: environment variables (upper case) and query or flag
	// parameters (lower case). Mixed case names (ssh's -o StrictHostKeyChecking=no) are options,
	// not secrets, and are left alone.
	envPair = regexp.MustCompile(`\b([A-Z0-9_]*(?:KEY|TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIALS?|AUTH)[A-Z0-9_]*|[a-z0-9_]*(?:token|secret|password|passwd|api_?key|access_?key|private_?key|credentials?|auth)[a-z0-9_]*)=("[^"]*"|'[^']*'|\S+)`)
	// name: value or name = value at the start of a line (YAML, INI, kubeconfig).
	linePair = regexp.MustCompile(`(?m)^(\s*(?:-\s+)?(?:export\s+)?["']?)([A-Za-z0-9_.\-]+)(["']?[ \t]*[:=][ \t]*)(\S.*)$`)
	// Authorization headers and bearer tokens.
	bearer = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=\-]{8,}`)
)

// Text redacts free text: logs, YAML, command output.
func (r Redactor) Text(s string) string {
	s = pemKey.ReplaceAllString(s, redacted+" private key")
	s = jsonPair.ReplaceAllStringFunc(s, func(m string) string {
		p := jsonPair.FindStringSubmatch(m)
		if !secretName.MatchString(p[1]) || p[3] == "" {
			return m
		}
		return `"` + p[1] + `"` + p[2] + `"` + redacted + `"`
	})
	s = linePair.ReplaceAllStringFunc(s, func(m string) string {
		p := linePair.FindStringSubmatch(m)
		v := strings.TrimSpace(p[4])
		if !secretName.MatchString(p[2]) || strings.Contains(v, redacted) || v == "|" || v == ">" || v == "{" || v == "[" {
			return m
		}
		tail := ""
		if strings.HasSuffix(v, ",") {
			tail = ","
		}
		return p[1] + p[2] + p[3] + redacted + tail
	})
	s = envPair.ReplaceAllStringFunc(s, func(m string) string {
		p := envPair.FindStringSubmatch(m)
		if strings.Contains(p[2], redacted) {
			return m
		}
		return p[1] + "=" + redacted
	})
	s = bearer.ReplaceAllString(s, "$1 "+redacted)
	return r.plain(s)
}

// plain replaces the token, the home folder and the user name, the parts of a redaction that
// apply to every file whatever its format.
func (r Redactor) plain(s string) string {
	if len(r.token) >= 8 {
		s = strings.ReplaceAll(s, r.token, "<session-token>")
	}
	if len(r.home) > 1 {
		s = strings.ReplaceAll(s, r.home, "~")
	}
	if r.user != nil {
		s = r.user.ReplaceAllString(s, "<user>")
	}
	return s
}

// JSON redacts a JSON document by structure: under a key with a secret's name every string is
// replaced, and every other string is redacted as text. Not JSON: redacted as text.
func (r Redactor) JSON(b []byte) []byte {
	var v any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return []byte(r.Text(string(b)))
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false) // keep <redacted> readable
	enc.SetIndent("", "  ")
	if err := enc.Encode(r.walk(v, false)); err != nil {
		return []byte(r.Text(string(b)))
	}
	return out.Bytes()
}

// Value marshals v and redacts it as JSON.
func (r Redactor) Value(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(r.Text(err.Error()))
	}
	return r.JSON(b)
}

func (r Redactor) walk(v any, secret bool) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[r.plain(k)] = r.walk(x, secret || secretName.MatchString(k))
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = r.walk(x, secret)
		}
		return out
	case string:
		if secret && t != "" {
			return redacted
		}
		return r.Text(t)
	case json.Number:
		if secret {
			return redacted
		}
		return t
	default:
		return v
	}
}
