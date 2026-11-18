package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// Fixed test values, not secrets.
var testRedactor = NewRedactor("/Users/alice", "alice", "feedface0123456789abcdef")

func mustNotContain(t *testing.T, got string, bad ...string) {
	t.Helper()
	for _, b := range bad {
		if strings.Contains(got, b) {
			t.Errorf("%q still in:\n%s", b, got)
		}
	}
}

func mustContain(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("%q missing from:\n%s", w, got)
		}
	}
}

func TestRedactColimaYAML(t *testing.T) {
	in := `cpu: 2
memory: 2
mounts:
  - location: /Users/alice/src
    writable: true
env:
  AWS_SECRET_ACCESS_KEY: wJalrXUtnFEMI
  GITHUB_TOKEN: "ghp_abcdef123456"
  DB_PASSWORD=hunter2
  PATH: /usr/bin
docker:
  registry-mirrors: []
`
	got := testRedactor.Text(in)
	mustNotContain(t, got, "wJalrXUtnFEMI", "ghp_abcdef123456", "hunter2", "/Users/alice")
	mustContain(t, got, "AWS_SECRET_ACCESS_KEY: <redacted>", "GITHUB_TOKEN: <redacted>", "location: ~/src", "PATH: /usr/bin", "cpu: 2")
}

func TestRedactKubeconfig(t *testing.T) {
	in := `users:
- name: colima
  user:
    client-certificate-data: LS0tLS1CRUdJTiBDRVJU
    client-key-data: LS0tLS1CRUdJTiBSU0Eg
    token: abc.def.ghi
clusters:
- cluster:
    server: https://127.0.0.1:6443
`
	got := testRedactor.Text(in)
	mustNotContain(t, got, "LS0tLS1CRUdJTiBDRVJU", "LS0tLS1CRUdJTiBSU0Eg", "abc.def.ghi")
	mustContain(t, got, "server: https://127.0.0.1:6443", "client-key-data: <redacted>")
}

func TestRedactDockerConfigJSON(t *testing.T) {
	in := `{"auths":{"https://index.docker.io/v1/":{"auth":"YWxpY2U6czNjcjN0"},"ghcr.io":{"auth":"Z2hjcg==","identitytoken":"tok"}},"credsStore":"osxkeychain","currentContext":"colima"}`
	got := string(testRedactor.JSON([]byte(in)))
	mustNotContain(t, got, "YWxpY2U6czNjcjN0", "Z2hjcg==", `"tok"`)
	mustContain(t, got, "https://index.docker.io/v1/", "osxkeychain", `"currentContext": "colima"`)
	var v map[string]any
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("not JSON any more: %v\n%s", err, got)
	}
}

func TestRedactJSONEnvAndPaths(t *testing.T) {
	in := map[string]any{
		"Env":        []string{"API_KEY=sk-live-1234", "HOME=/root", "LANG=C"},
		"DockerRoot": "/Users/alice/.colima/default",
		"Password":   "pw",
		"Port":       7766,
		"Session":    "feedface0123456789abcdef",
		"Detail":     "open http://127.0.0.1:7766/?token=feedface0123456789abcdef", // fake test value; pragma:allow-secret
	}
	got := string(testRedactor.Value(in))
	mustNotContain(t, got, "sk-live-1234", `"pw"`, "feedface0123456789abcdef", "/Users/alice")
	mustContain(t, got, "API_KEY=<redacted>", "HOME=/root", "~/.colima/default", "7766")
}

func TestRedactLogsAndKeys(t *testing.T) {
	in := strings.Join([]string{
		`{"level":"info","msg":"Forwarding /Users/alice/.colima/default/docker.sock","time":"2026-11-18T10:00:00Z"}`,
		`{"level":"debug","token":"s3cr3t-value","time":"2026-11-18T10:00:01Z"}`,
		`curl -H "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig" https://example.test`,
		`running as user alice on alice-mbp`,
		`-----BEGIN OPENSSH PRIVATE KEY-----`, // fake test value; pragma:allow-secret
		`b3BlbnNzaC1rZXktdjEAAAAA`,
		`-----END OPENSSH PRIVATE KEY-----`,
		`ssh-ed25519 AAAAC3Nza public key stays`,
		`time="2026-11-18" level=info msg="started"`,
		`/usr/bin/ssh -o PreferredAuthentications=publickey -o StrictHostKeyChecking=no -p 22`,
		`GET /v1/x?access_token=abc123&page=2`,
	}, "\n")
	got := testRedactor.Text(in)
	mustNotContain(t, got, "/Users/alice", "s3cr3t-value", "eyJhbGciOiJIUzI1NiJ9", "b3BlbnNzaC1rZXktdjEAAAAA", "user alice")
	mustContain(t, got, "~/.colima/default/docker.sock", `"token":"<redacted>"`, "Bearer <redacted>", "<redacted> private key",
		"user <user> on <user>-mbp", "ssh-ed25519 AAAAC3Nza public key stays", `level=info msg="started"`,
		"PreferredAuthentications=publickey -o StrictHostKeyChecking=no", "access_token=<redacted>")
	mustNotContain(t, got, "abc123")
}

func TestRedactLeavesOrdinaryTextAlone(t *testing.T) {
	in := "colima version 0.10.3\ngit commit: abc123\nruntime: docker\narch: aarch64\n"
	if got := testRedactor.Text(in); got != in {
		t.Fatalf("changed ordinary text:\n%s", got)
	}
	short := NewRedactor("", "", "")
	if got := short.Text("API_KEY=x"); got != "API_KEY=<redacted>" {
		t.Fatalf("empty redactor: %q", got)
	}
}
