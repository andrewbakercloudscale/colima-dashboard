package server

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/colima"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/docker"
)

// The Errors page: error and warning lines from every container's and every pod's logs in one
// list, newest first, so a problem shows without opening each log in turn.

const (
	errTailPerSource = 3000 // lines read per container or pod
	errMaxLines      = 1500 // lines sent to the page
	errCacheTTL      = 10 * time.Second
)

// levelOf says whether a log line reports an error ("error"), a warning ("warn") or neither ("").
// It knows level fields (level=error, "level":"warn", [ERROR], E0102 klog), and the words a
// failure prints (panic, fatal, exception, traceback, out of memory).
func levelOf(line string) string {
	l := strings.ToLower(line)
	if len(l) > 2000 {
		l = l[:2000]
	}
	switch {
	case errNoise.MatchString(l):
		return ""
	case errLevel.MatchString(l), klogErr.MatchString(line), errWords.MatchString(l):
		return "error"
	case warnLevel.MatchString(l), klogWarn.MatchString(line), warnWords.MatchString(l):
		return "warn"
	}
	return ""
}

var (
	// "0 errors", "no errors", "error_log", "errors=0": words that look like trouble but are not.
	errNoise  = regexp.MustCompile(`\b(0|no|zero) (errors?|failures?|warnings?)\b|\berrors?[=:]\s*0\b|\b(error_log|errorlog|on-error|onerror)\b`)
	errLevel  = regexp.MustCompile(`\blevel[=:]\s*"?(error|err|fatal|panic|crit|critical)\b|"(level|severity|lvl)"\s*:\s*"(error|err|fatal|panic|critical)"|\[(error|err|fatal|crit|critical|emerg|alert)\]|^\s*(error|fatal|panic|critical)\b|\s(error|fatal|err)\s*[:|\]]`)
	errWords  = regexp.MustCompile(`\b(fatal error|exceptions?\b|segmentation fault|out of memory|oom[- ]?kill|stack trace|unhandled|uncaught|failed to\b|connection refused|permission denied)|panic:|traceback \(most recent call last\)`)
	warnLevel = regexp.MustCompile(`\blevel[=:]\s*"?(warn|warning)\b|"(level|severity|lvl)"\s*:\s*"(warn|warning)"|\[(warn|warning)\]|^\s*(warn|warning)\b|\s(warn|warning)\s*[:|\]]`)
	warnWords = regexp.MustCompile(`\b(deprecated|retrying|timed out|timeout)\b`)
	klogErr   = regexp.MustCompile(`^[EF]\d{4} \d\d:\d\d:\d\d`)
	klogWarn  = regexp.MustCompile(`^W\d{4} \d\d:\d\d:\d\d`)
)

type errLine struct {
	TS     string `json:"ts"`
	Source string `json:"source"` // the key of its errSource
	Level  string `json:"level"`
	Text   string `json:"text"`
}

type errSource struct {
	Key       string `json:"key"`
	Kind      string `json:"kind"` // container | pod
	Name      string `json:"name"`
	ID        string `json:"id,omitempty"`        // container id
	Project   string `json:"project,omitempty"`   // compose project
	Namespace string `json:"namespace,omitempty"` // pod namespace
	State     string `json:"state,omitempty"`
	Errors    int    `json:"errors"`
	Warnings  int    `json:"warnings"`
	Last      string `json:"last,omitempty"` // newest error or warning
}

type errResponse struct {
	Profile   string       `json:"profile"`
	Since     int64        `json:"since"` // ms
	At        int64        `json:"at"`
	Sources   []*errSource `json:"sources"`
	Lines     []errLine    `json:"lines"`
	Truncated bool         `json:"truncated"`
	Scanned   int          `json:"scanned"` // containers and pods read
	Errors    int          `json:"errors"`
	Warnings  int          `json:"warnings"`
	Pods      bool         `json:"pods"`               // Kubernetes pods were read
	PodError  string       `json:"podError,omitempty"` // why they could not be
}

type errCache struct {
	mu sync.Mutex
	m  map[string]errResponse
}

func (s *Server) errorRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/profiles/{p}/errors", s.auth(s.apiErrors))
}

// apiErrors: ?since=<seconds> (default an hour, at most a week).
func (s *Server) apiErrors(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("p")
	dc, err := s.dockerFor(p)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	secs, _ := strconv.Atoi(r.URL.Query().Get("since"))
	if secs <= 0 || secs > 7*86400 {
		secs = 3600
	}
	key := p + "/" + strconv.Itoa(secs)
	s.errs.mu.Lock()
	if c, ok := s.errs.m[key]; ok && time.Since(time.UnixMilli(c.At)) < errCacheTTL {
		s.errs.mu.Unlock()
		writeJSON(w, http.StatusOK, c)
		return
	}
	s.errs.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	res, err := s.collectErrors(ctx, p, dc, time.Duration(secs)*time.Second)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	s.errs.mu.Lock()
	if s.errs.m == nil {
		s.errs.m = map[string]errResponse{}
	}
	s.errs.m[key] = res
	s.errs.mu.Unlock()
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) collectErrors(ctx context.Context, p string, dc *docker.Client, window time.Duration) (errResponse, error) {
	now := time.Now()
	since := now.Add(-window)
	res := errResponse{Profile: p, Since: since.UnixMilli(), At: now.UnixMilli(), Sources: []*errSource{}, Lines: []errLine{}}
	st, err := s.opt.Colima.Status(ctx, p)
	if err != nil {
		return res, errors.New("profile " + p + " is not running")
	}
	var mu sync.Mutex
	var lines []errLine
	add := func(src *errSource, ts, text string) {
		lv := levelOf(text)
		if lv == "" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if src.Errors+src.Warnings == 0 {
			res.Sources = append(res.Sources, src)
		}
		if lv == "error" {
			src.Errors++
		} else {
			src.Warnings++
		}
		if ts > src.Last {
			src.Last = ts
		}
		if len(text) > 1000 {
			text = text[:1000] + "…"
		}
		lines = append(lines, errLine{TS: ts, Source: src.Key, Level: lv, Text: text})
	}

	var wg sync.WaitGroup
	if st.Runtime == "docker" || strings.HasPrefix(st.Runtime, "docker") {
		cs, err := dc.Containers(ctx)
		if err != nil {
			return res, err
		}
		sem := make(chan struct{}, 6)
		for _, c := range cs {
			if c.Labels["io.kubernetes.pod.name"] != "" {
				continue // k3s with the docker runtime: read as a pod below
			}
			src := &errSource{Key: "c:" + c.ID[:min(12, len(c.ID))], Kind: "container", ID: c.ID,
				Name: strings.TrimPrefix(firstName(c.Names), "/"), Project: c.Labels["com.docker.compose.project"], State: c.State}
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				doc, err := dc.Inspect(ctx, c.ID)
				if err != nil {
					return
				}
				tty := false
				if cfg, ok := doc["Config"].(map[string]any); ok {
					tty, _ = cfg["Tty"].(bool)
				}
				if c.State != "running" {
					if st, ok := doc["State"].(map[string]any); ok {
						fin, _ := st["FinishedAt"].(string)
						if t, err := time.Parse(time.RFC3339Nano, fin); err == nil && t.Before(since) {
							return // stopped before the window: nothing new to read
						}
					}
				}
				mu.Lock()
				res.Scanned++
				mu.Unlock()
				_ = dc.Logs(ctx, c.ID, errTailPerSource, since, false, true, tty, func(_, line string) error {
					ts, text, _ := docker.SplitStamp(line)
					add(src, ts, text)
					return nil
				})
			}()
		}
	}
	if st.Kubernetes || strings.Contains(st.Runtime, "k3s") {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pods, err := s.podErrorLogs(ctx, p, window, &mu, &res, add)
			mu.Lock()
			res.Pods = pods
			if err != nil {
				res.PodError = err.Error()
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	sort.Slice(lines, func(i, j int) bool { return lines[i].TS > lines[j].TS })
	for _, l := range lines {
		if l.Level == "error" {
			res.Errors++
		} else {
			res.Warnings++
		}
	}
	if len(lines) > errMaxLines {
		lines, res.Truncated = lines[:errMaxLines], true
	}
	res.Lines = lines
	sort.SliceStable(res.Sources, func(i, j int) bool {
		a, b := res.Sources[i], res.Sources[j]
		if a.Errors != b.Errors {
			return a.Errors > b.Errors
		}
		if a.Warnings != b.Warnings {
			return a.Warnings > b.Warnings
		}
		return a.Name < b.Name
	})
	return res, nil
}

// podScript prints "@@POD ns name phase" before each pod's logs; one ssh session for them all.
const podScript = `k='sudo k3s kubectl'; $k get pods -A --no-headers -o custom-columns=NS:.metadata.namespace,N:.metadata.name,P:.status.phase 2>&1 | ` +
	`while read ns n ph; do echo "@@POD $ns $n $ph"; $k logs -n "$ns" "$n" --all-containers --prefix --timestamps --since="$1" --tail=` +
	`"$2" 2>/dev/null; done`

// podErrorLogs reads every pod's recent logs inside the VM.
func (s *Server) podErrorLogs(ctx context.Context, p string, window time.Duration, mu *sync.Mutex, res *errResponse, add func(*errSource, string, string)) (bool, error) {
	if !colima.ValidProfile(p) {
		return false, errors.New("invalid profile")
	}
	cmd, err := s.opt.Colima.SSHCommand(ctx, p, "sh", "-c", podScript, "sh",
		strconv.Itoa(int(window.Seconds()))+"s", strconv.Itoa(errTailPerSource))
	if err != nil {
		return false, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return false, err
	}
	if err := cmd.Start(); err != nil {
		return false, err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	var cur *errSource
	seen := false
	for sc.Scan() {
		line := sc.Text()
		if rest, ok := strings.CutPrefix(line, "@@POD "); ok {
			f := strings.Fields(rest)
			if len(f) < 2 {
				cur = nil
				continue
			}
			cur = &errSource{Key: "p:" + f[0] + "/" + f[1], Kind: "pod", Namespace: f[0], Name: f[1]}
			if len(f) > 2 {
				cur.State = strings.ToLower(f[2])
			}
			mu.Lock()
			res.Scanned++
			mu.Unlock()
			seen = true
			continue
		}
		if cur == nil {
			continue
		}
		// [pod/name/container] 2026-11-18T10:00:00.123Z text
		text := line
		if strings.HasPrefix(text, "[") {
			if i := strings.Index(text, "] "); i > 0 {
				text = text[i+2:]
			}
		}
		ts, body, _ := docker.SplitStamp(text)
		add(cur, ts, body)
	}
	err = cmd.Wait()
	if !seen && err != nil && ctx.Err() == nil {
		return false, errors.New("could not read pod logs: " + err.Error())
	}
	return seen, nil
}
