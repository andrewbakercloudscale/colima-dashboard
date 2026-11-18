package bloat

import (
	"fmt"
	"sort"
	"strings"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/docker"
)

// Layer is one build step, oldest first.
type Layer struct {
	Kind string   `json:"kind"`
	Step string   `json:"step"`
	Size int64    `json:"size"`
	Tags []string `json:"tags,omitempty"`
}

// Finding is one way the image wastes space and how to fix it.
type Finding struct {
	ID       string `json:"id"`
	Severity string `json:"severity"` // high, medium, low
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Fix      string `json:"fix,omitempty"`
	Saving   int64  `json:"saving,omitempty"`
}

// DirSize is a folder and the bytes under it.
type DirSize struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Depth int    `json:"depth"`
}

// Report is the bloat check for one image.
type Report struct {
	Ref          string    `json:"ref"`
	ID           string    `json:"id"`
	Size         int64     `json:"size"`
	Deep         bool      `json:"deep"`
	Score        int       `json:"score"`
	Grade        string    `json:"grade"`
	Saving       int64     `json:"saving"`
	Wasted       int64     `json:"wasted"`
	Files        int       `json:"files"`
	Layers       []Layer   `json:"layers"`
	LayerCount   int       `json:"layerCount"`
	Base         string    `json:"base"`
	BaseKind     string    `json:"baseKind"`
	Stack        string    `json:"stack"`
	User         string    `json:"user"`
	Arch         string    `json:"arch"`
	Findings     []Finding `json:"findings"`
	Dirs         []DirSize `json:"dirs"`
	BigFiles     []File    `json:"bigFiles"`
	Tools        []string  `json:"tools"`
	Dockerfile   string    `json:"dockerfile"`
	Dockerignore string    `json:"dockerignore"`
}

const mb = 1 << 20

var stepKinds = map[string]bool{"RUN": true, "COPY": true, "ADD": true, "ENV": true, "CMD": true, "ENTRYPOINT": true,
	"WORKDIR": true, "LABEL": true, "EXPOSE": true, "USER": true, "ARG": true, "VOLUME": true, "HEALTHCHECK": true,
	"SHELL": true, "ONBUILD": true, "STOPSIGNAL": true, "MAINTAINER": true}

// CleanStep turns a history CreatedBy into the Dockerfile line it came from.
func CleanStep(s string) (kind, text string) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "# buildkit"))
	if s == "" {
		return "", "(imported layer, no build step recorded)"
	}
	if i := strings.Index(s, "#(nop)"); i >= 0 {
		s = strings.TrimSpace(s[i+len("#(nop)"):])
	} else {
		rest := strings.TrimPrefix(s, "RUN ")
		if strings.HasPrefix(rest, "|") || strings.HasPrefix(rest, "/bin/sh -c ") {
			if i := strings.Index(rest, "/bin/sh -c "); i >= 0 {
				s = "RUN " + rest[i+len("/bin/sh -c "):]
			}
		}
	}
	s = strings.Join(strings.Fields(s), " ")
	first, _, _ := strings.Cut(s, " ")
	if stepKinds[strings.ToUpper(first)] {
		return strings.ToUpper(first), s
	}
	return "RUN", "RUN " + s
}

// Analyze builds the report. tree is nil for a quick check from the build history alone.
func Analyze(ref string, img *docker.ImageConfig, hist []docker.HistoryEntry, tree *Tree) *Report {
	r := &Report{Ref: ref, ID: img.ID, Size: img.Size, Deep: tree != nil, User: img.Config.User, Arch: img.Architecture,
		Findings: []Finding{}, Dirs: []DirSize{}, BigFiles: []File{}, Tools: []string{}, Layers: []Layer{}}
	var sum int64
	for i := len(hist) - 1; i >= 0; i-- {
		h := hist[i]
		k, t := CleanStep(h.CreatedBy)
		r.Layers = append(r.Layers, Layer{Kind: k, Step: t, Size: h.Size, Tags: h.Tags})
		sum += h.Size
		if h.Size > 0 {
			r.LayerCount++
		}
	}
	env := map[string]string{}
	for _, e := range img.Config.Env {
		k, v, _ := strings.Cut(e, "=")
		env[k] = v
	}
	r.Stack = detectStack(env, r.Layers, tree)
	r.Base, r.BaseKind = detectBase(r.Layers, tree)
	if sum > r.Size {
		r.Size = sum // inspect reports the compressed size on the containerd image store
	}
	if tree != nil {
		r.Files = tree.Files
		if w := sum - tree.Total; w > 0 {
			r.Wasted = w
		}
		r.Dirs = dirList(tree)
		r.BigFiles = append(r.BigFiles, tree.Big...)
		for k := range tree.Has {
			r.Tools = append(r.Tools, k)
		}
		sort.Strings(r.Tools)
	}
	a := analyzer{r: r, env: env, tree: tree}
	a.history()
	if tree != nil {
		a.files()
	}
	a.score()
	r.Dockerfile, r.Dockerignore = Suggest(r.Stack, r.BaseKind)
	return r
}

type analyzer struct {
	r    *Report
	env  map[string]string
	tree *Tree
}

func (a *analyzer) add(f Finding) { a.r.Findings = append(a.r.Findings, f) }

func (a *analyzer) runs() []Layer {
	var out []Layer
	for _, l := range a.r.Layers {
		if l.Kind == "RUN" {
			out = append(out, l)
		}
	}
	return out
}

func (a *analyzer) history() {
	var aptRec, aptLists, apkCache, pipCache, npmDev, cleanupAlone bool
	var chown Layer
	var copyAll Layer
	for _, l := range a.runs() {
		s := strings.ToLower(l.Step)
		if (strings.Contains(s, "apt-get install") || strings.Contains(s, "apt install")) && !strings.Contains(s, "--no-install-recommends") {
			aptRec = true
		}
		if (strings.Contains(s, "apt-get update") || strings.Contains(s, "apt update")) && !strings.Contains(s, "/var/lib/apt/lists") {
			aptLists = true
		}
		if strings.Contains(s, "apk add") && !strings.Contains(s, "--no-cache") {
			apkCache = true
		}
		if (strings.Contains(s, "pip install") || strings.Contains(s, "pip3 install")) && !strings.Contains(s, "--no-cache-dir") &&
			a.env["PIP_NO_CACHE_DIR"] == "" && !strings.Contains(s, "type=cache") && !strings.Contains(s, ".cache") {
			pipCache = true
		}
		if (strings.Contains(s, "npm install") || strings.Contains(s, "npm i ") || strings.HasSuffix(s, "npm i") || strings.Contains(s, "yarn install") || strings.Contains(s, "npm ci")) &&
			!strings.Contains(s, "--omit=dev") && !strings.Contains(s, "--production") && !strings.Contains(s, "--only=prod") &&
			a.env["NODE_ENV"] != "production" && !strings.Contains(s, " -g ") && !strings.Contains(s, "--global") {
			npmDev = true
		}
		if onlyCmds(s, "chown", "chmod", "mkdir", "set ", "true") && strings.Contains(s, "chown -r") && l.Size > 10*mb && l.Size > chown.Size {
			chown = l
		}
		body := strings.TrimPrefix(s, "run ")
		if l.Size < mb && (strings.HasPrefix(body, "rm ") || strings.HasPrefix(body, "apt-get purge") || strings.HasPrefix(body, "apt-get remove") ||
			strings.HasPrefix(body, "apt-get autoremove") || strings.HasPrefix(body, "apk del") || strings.HasPrefix(body, "npm cache clean") ||
			strings.HasPrefix(body, "apt-get clean")) {
			cleanupAlone = true
		}
	}
	for _, l := range a.r.Layers {
		s := strings.ToLower(l.Step)
		if (l.Kind == "COPY" || l.Kind == "ADD") && (strings.HasPrefix(s, "copy . ") || strings.HasPrefix(s, "add . ") || strings.HasPrefix(s, "copy dir:")) && l.Size > 20*mb && l.Size > copyAll.Size {
			copyAll = l
		}
	}
	t := a.tree
	if aptRec {
		a.add(Finding{ID: "apt-recommends", Severity: "medium", Title: "apt installs recommended extras",
			Detail: "apt-get install runs without --no-install-recommends, so every suggested package comes too. This often adds tens of MB.",
			Fix:    "RUN apt-get update \\\n && apt-get install -y --no-install-recommends <packages> \\\n && rm -rf /var/lib/apt/lists/*"})
	}
	if aptLists && (t == nil || t.Dir("/var/lib/apt/lists") > mb) {
		a.add(Finding{ID: "apt-lists", Severity: "medium", Title: "apt package lists left behind",
			Detail: "apt-get update downloads package lists that stay in the image unless they are removed in the same RUN.",
			Fix:    "RUN apt-get update && apt-get install -y --no-install-recommends <packages> \\\n && rm -rf /var/lib/apt/lists/*",
			Saving: t.Dir("/var/lib/apt/lists")})
	}
	if apkCache && (t == nil || t.Dir("/var/cache/apk") > 512<<10) {
		a.add(Finding{ID: "apk-cache", Severity: "low", Title: "apk cache kept",
			Detail: "apk add without --no-cache keeps the package index in the image.",
			Fix:    "RUN apk add --no-cache <packages>", Saving: t.Dir("/var/cache/apk")})
	}
	if pipCache && (t == nil || t.Dir("/root/.cache/pip") > mb) {
		a.add(Finding{ID: "pip-cache", Severity: "medium", Title: "pip download cache kept",
			Detail: "pip install keeps every downloaded wheel in /root/.cache/pip.",
			Fix:    "ENV PIP_NO_CACHE_DIR=1\nRUN pip install -r requirements.txt", Saving: t.Dir("/root/.cache/pip")})
	}
	if npmDev {
		f := Finding{ID: "npm-dev", Severity: "medium", Title: "Dev dependencies probably shipped",
			Detail: "npm install runs without --omit=dev and NODE_ENV is not production, so test tools, bundlers and type packages end up in the runtime image.",
			Fix:    "RUN --mount=type=cache,target=/root/.npm npm ci --omit=dev"}
		if t != nil && t.Named["node_modules"] > 0 {
			f.Detail += fmt.Sprintf(" node_modules holds %s here.", human(t.Named["node_modules"]))
		}
		a.add(f)
	}
	if chown.Size > 0 {
		a.add(Finding{ID: "chown-layer", Severity: "high", Title: "chown -R copies every file again",
			Detail: fmt.Sprintf("A separate RUN chown -R writes a second copy of each file it touches (%s here). The first copy still ships in the earlier layer.", human(chown.Size)),
			Fix:    "COPY --chown=app:app . /app", Saving: chown.Size})
	}
	if cleanupAlone {
		a.add(Finding{ID: "cleanup-layer", Severity: "medium", Title: "Clean-up runs in its own step",
			Detail: "Deleting files in a later RUN only hides them; the bytes still ship in the layer that added them. Clean up in the same RUN that creates the files.",
			Fix:    "RUN <install or build> \\\n && <clean up> \\\n && rm -rf /tmp/* /var/lib/apt/lists/*"})
	}
	if copyAll.Size > 0 {
		f := Finding{ID: "copy-all", Severity: "medium", Title: "Whole build folder copied in",
			Detail: fmt.Sprintf("%s adds %s. Without a .dockerignore, .git, local node_modules, logs and build output all go in.", copyAll.Step, human(copyAll.Size)),
			Fix:    "# .dockerignore\n.git\nnode_modules\n*.log\n.env*"}
		a.add(f)
	}
	if a.r.LayerCount > 25 {
		a.add(Finding{ID: "many-layers", Severity: "low", Title: fmt.Sprintf("%d layers", a.r.LayerCount),
			Detail: "Many small RUN steps add overhead and make clean-up in a later step useless. Join related commands with &&.",
			Fix:    "RUN step-one \\\n && step-two \\\n && clean-up"})
	}
	if u := strings.ToLower(a.r.User); u == "" || u == "root" || u == "0" || strings.HasPrefix(u, "0:") || strings.HasPrefix(u, "root:") {
		a.add(Finding{ID: "root", Severity: "low", Title: "Runs as root",
			Detail: "Not a size problem, but a smaller attack surface goes with a smaller image: run as a normal user.",
			Fix:    "USER 1000"})
	}
}

// toolchainDirs are folders that only a build needs.
var toolchainDirs = []string{"/usr/local/go", "/usr/lib/gcc", "/usr/libexec/gcc", "/usr/local/cargo", "/usr/local/rustup",
	"/root/.cargo", "/root/.rustup", "/root/.m2", "/root/.gradle", "/usr/share/maven", "/usr/include", "/go/pkg", "/root/go/pkg",
	"/usr/share/dotnet/sdk", "/usr/lib/llvm-14", "/usr/lib/llvm-15", "/usr/lib/llvm-16"}

// cacheDirs are package manager caches that never need to ship.
var cacheDirs = []string{"/var/lib/apt/lists", "/var/cache/apt", "/var/cache/apk", "/var/cache/dnf", "/var/cache/yum",
	"/root/.cache", "/root/.npm", "/usr/local/share/.cache", "/home/node/.npm", "/home/node/.cache", "/tmp", "/var/tmp"}

func (a *analyzer) files() {
	t := a.tree
	var tc int64
	var tcWhat []string
	for _, d := range toolchainDirs {
		if n := t.Dir(d); n > mb {
			tc += n
			tcWhat = append(tcWhat, fmt.Sprintf("%s (%s)", d, human(n)))
		}
	}
	if t.Has["javac"] {
		for _, d := range []string{"/usr/lib/jvm", "/opt/java/openjdk"} {
			if n := t.Dir(d); n > 0 {
				jre := n * 45 / 100 // a JRE is roughly half of a JDK
				tc += n - jre
				tcWhat = append(tcWhat, fmt.Sprintf("a full JDK in %s (%s)", d, human(n)))
			}
		}
	}
	var compilers []string
	for _, c := range []string{"gcc", "g++", "go", "cargo", "rustc", "javac", "mvn", "gradle", "make"} {
		if t.Has[c] {
			compilers = append(compilers, c)
		}
	}
	if tc > 20*mb || len(compilers) > 0 && tc > 0 {
		sev := "medium"
		if tc > 100*mb {
			sev = "high"
		}
		detail := "Compilers and build caches are only needed to build. A multi-stage build compiles in one stage and copies just the result into a small runtime stage."
		if len(tcWhat) > 0 {
			detail += " Found: " + strings.Join(tcWhat, ", ") + "."
		}
		if len(compilers) > 0 {
			detail += " Build tools present: " + strings.Join(compilers, ", ") + "."
		}
		a.add(Finding{ID: "toolchain", Severity: sev, Title: "Build tools ship in the final image (no multi-stage build)",
			Detail: detail, Fix: "FROM <build image> AS build\n# ... build here ...\nFROM <slim or distroless image>\nCOPY --from=build /out/app /app", Saving: tc})
	}
	var cache int64
	var cacheWhat []string
	for _, d := range cacheDirs {
		if n := t.Dir(d); n > 512<<10 {
			cache += n
			cacheWhat = append(cacheWhat, fmt.Sprintf("%s (%s)", d, human(n)))
		}
	}
	if cache > 5*mb {
		sev := "medium"
		if cache > 100*mb {
			sev = "high"
		}
		a.add(Finding{ID: "caches", Severity: sev, Title: "Package caches and temp files ship",
			Detail: "Found " + strings.Join(cacheWhat, ", ") + ". Remove them in the same RUN that creates them, or use a BuildKit cache mount so they never land in a layer.",
			Fix:    "RUN --mount=type=cache,target=/root/.cache \\\n    --mount=type=cache,target=/var/cache/apt \\\n    <install step>", Saving: cache})
	}
	wasted := a.r.Wasted
	for _, f := range a.r.Findings {
		if f.ID == "chown-layer" {
			wasted -= f.Saving // the chown copy is already counted
		}
	}
	if wasted > 20*mb {
		sev := "medium"
		if wasted > 100*mb {
			sev = "high"
		}
		have := false
		for i, f := range a.r.Findings {
			if f.ID == "cleanup-layer" {
				a.r.Findings[i].Severity, a.r.Findings[i].Saving = sev, wasted
				have = true
			}
		}
		if !have {
			a.add(Finding{ID: "wasted", Severity: sev, Title: "Deleted or overwritten files still ship",
				Detail: fmt.Sprintf("The layers add up to %s more than the final filesystem. Files removed or changed in a later step stay in the earlier layer.", human(wasted)),
				Fix:    "RUN <download or build> \\\n && <use it> \\\n && rm -rf <what you no longer need>", Saving: wasted})
		}
	}
	if g := t.Named[".git"]; g > mb {
		a.add(Finding{ID: "git", Severity: "medium", Title: ".git history copied in",
			Detail: fmt.Sprintf("%s of git history is inside the image. Add .git to .dockerignore.", human(g)), Fix: "# .dockerignore\n.git", Saving: g})
	}
	if p := t.Named["__pycache__"]; p > 5*mb {
		a.add(Finding{ID: "pycache", Severity: "low", Title: "Python bytecode caches",
			Detail: fmt.Sprintf("__pycache__ folders hold %s.", human(p)), Fix: "ENV PYTHONDONTWRITEBYTECODE=1", Saving: p})
	}
	if tests := t.Named["test"] + t.Named["tests"]; tests > 20*mb {
		a.add(Finding{ID: "tests", Severity: "low", Title: "Test folders ship",
			Detail: fmt.Sprintf("Folders named test or tests hold %s, often inside dependencies.", human(tests)), Saving: tests / 2})
	}
	var arch int64
	var archWhat []string
	for _, f := range t.Archives {
		arch += f.Size
		archWhat = append(archWhat, f.Path)
	}
	if arch > 5*mb {
		a.add(Finding{ID: "archives", Severity: "medium", Title: "Downloaded archives left in the image",
			Detail: fmt.Sprintf("%s in %s. Unpack and delete in the same RUN, or download in a build stage.", human(arch), strings.Join(archWhat, ", ")),
			Fix:    "RUN curl -fsSL <url> -o /tmp/x.tgz \\\n && tar -xzf /tmp/x.tgz -C /opt \\\n && rm /tmp/x.tgz", Saving: arch})
	}
	docs := t.Dir("/usr/share/doc") + t.Dir("/usr/share/man") + t.Dir("/usr/share/info") + t.Dir("/usr/share/locale")
	if docs > 15*mb {
		a.add(Finding{ID: "docs", Severity: "low", Title: "Manuals, docs and translations",
			Detail: fmt.Sprintf("%s of docs, man pages and locales. Slim and distroless bases leave them out.", human(docs)), Saving: docs})
	}
	if a.r.BaseKind == "full" && a.r.Size > 250*mb {
		a.add(Finding{ID: "fat-base", Severity: "medium", Title: "Full distribution base",
			Detail: "The image is built on a full " + a.r.Base + " base. A -slim tag, Alpine or a distroless image is usually 50 to 90 percent smaller for the same app.",
			Fix:    slimHint(a.r.Stack)})
	}
	var dbg []string
	for _, c := range []string{"git", "curl", "wget", "vim", "nano", "ssh"} {
		if t.Has[c] {
			dbg = append(dbg, c)
		}
	}
	if len(dbg) > 1 && a.r.BaseKind != "full" {
		a.add(Finding{ID: "debug-tools", Severity: "low", Title: "Download and debug tools in the runtime image",
			Detail: "Present: " + strings.Join(dbg, ", ") + ". Use them in a build stage only; they add size and attack surface."})
	}
}

// onlyCmds reports whether every command in a RUN line starts with one of the given words.
func onlyCmds(s string, words ...string) bool {
	s = strings.TrimPrefix(s, "run ")
	for _, c := range strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == '&' || r == '|' }) {
		c = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(c), "\\"))
		if c == "" {
			continue
		}
		ok := false
		for _, w := range words {
			if strings.HasPrefix(c, w) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func slimHint(stack string) string {
	switch stack {
	case "node":
		return "FROM node:22-slim\n# or gcr.io/distroless/nodejs22-debian12 for the runtime stage"
	case "python":
		return "FROM python:3.12-slim"
	case "go":
		return "FROM gcr.io/distroless/static-debian12:nonroot"
	case "java":
		return "FROM eclipse-temurin:21-jre"
	case "rust":
		return "FROM gcr.io/distroless/cc-debian12:nonroot"
	}
	return "FROM debian:bookworm-slim"
}

func (a *analyzer) score() {
	r := a.r
	var save int64
	n := map[string]int{}
	for _, f := range r.Findings {
		save += f.Saving
		n[f.Severity]++
	}
	if r.Size > 0 && save > r.Size*9/10 {
		save = r.Size * 9 / 10
	}
	r.Saving = save
	s := 100.0 - 10*float64(n["high"]) - 5*float64(n["medium"]) - 1*float64(n["low"])
	if r.Size > 0 {
		s -= 60 * float64(save) / float64(r.Size)
	}
	if r.Size < 30*mb && s < 85 {
		s = 85
	}
	if s < 0 {
		s = 0
	}
	r.Score = int(s + 0.5)
	switch {
	case r.Score >= 90:
		r.Grade = "A"
	case r.Score >= 75:
		r.Grade = "B"
	case r.Score >= 60:
		r.Grade = "C"
	case r.Score >= 40:
		r.Grade = "D"
	default:
		r.Grade = "F"
	}
	rank := map[string]int{"high": 0, "medium": 1, "low": 2}
	sort.SliceStable(r.Findings, func(i, j int) bool {
		if rank[r.Findings[i].Severity] != rank[r.Findings[j].Severity] {
			return rank[r.Findings[i].Severity] < rank[r.Findings[j].Severity]
		}
		return r.Findings[i].Saving > r.Findings[j].Saving
	})
}

func detectStack(env map[string]string, layers []Layer, t *Tree) string {
	has := func(keys ...string) bool {
		for _, k := range keys {
			if _, ok := env[k]; ok {
				return true
			}
		}
		return false
	}
	switch {
	case has("GOLANG_VERSION", "GOPATH") || t != nil && t.Has["go"]:
		return "go"
	case has("NODE_VERSION") || t != nil && t.Has["node"]:
		return "node"
	case has("PYTHON_VERSION") || t != nil && t.Has["python3"] && t.Has["pip3"]:
		return "python"
	case has("JAVA_HOME", "JAVA_VERSION") || t != nil && t.Has["java"]:
		return "java"
	case has("RUSTUP_HOME", "CARGO_HOME", "RUST_VERSION") || t != nil && t.Has["cargo"]:
		return "rust"
	case has("DOTNET_VERSION", "ASPNET_VERSION", "ASPNETCORE_URLS") || t != nil && t.Has["dotnet"]:
		return "dotnet"
	case has("RUBY_VERSION") || t != nil && t.Has["ruby"]:
		return "ruby"
	case has("PHP_VERSION") || t != nil && t.Has["php"]:
		return "php"
	}
	if t != nil {
		return "" // build steps name tools the build used, not what the image runs
	}
	all := ""
	for _, l := range layers {
		all += strings.ToLower(l.Step) + "\n"
	}
	switch {
	case strings.Contains(all, "go build"):
		return "go"
	case strings.Contains(all, "npm ") || strings.Contains(all, "yarn "):
		return "node"
	case strings.Contains(all, "pip install"):
		return "python"
	case strings.Contains(all, "mvn ") || strings.Contains(all, "gradle"):
		return "java"
	case strings.Contains(all, "cargo build"):
		return "rust"
	}
	return ""
}

func detectBase(layers []Layer, t *Tree) (name, kind string) {
	if t != nil {
		id := osField(t.OSRelease, "ID")
		pretty := osField(t.OSRelease, "PRETTY_NAME")
		switch {
		case strings.Contains(strings.ToLower(pretty), "distroless"):
			return pretty, "distroless"
		case id == "alpine" || id == "wolfi" || id == "chainguard":
			return pretty, "alpine"
		case id == "":
			if t.Total < 30*mb && !t.Has["sh"] {
				return "scratch or distroless", "distroless"
			}
			return "unknown", ""
		case !t.Has["sh"] && !t.Has["bash"]:
			return pretty + " (no shell)", "distroless"
		}
		docs := t.Dir("/usr/share/doc") + t.Dir("/usr/share/man")
		if docs > 4*mb || t.Has["perl"] && t.Dir("/usr/share/perl") > 10*mb {
			return pretty, "full"
		}
		return pretty + " (slim)", "slim"
	}
	for _, l := range layers {
		if l.Size > 0 {
			if l.Kind == "ADD" && l.Size < 15*mb {
				return "small base (likely Alpine)", "alpine"
			}
			break
		}
	}
	return "", ""
}

func dirList(t *Tree) []DirSize {
	min := int64(mb)
	if t.Total/200 > min {
		min = t.Total / 200
	}
	var out []DirSize
	for p, n := range t.Dirs {
		d := strings.Count(p, "/")
		if d <= 3 && n >= min {
			out = append(out, DirSize{Path: p, Size: n, Depth: d})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Size > out[j].Size })
	if len(out) > 150 {
		out = out[:150]
	}
	return out
}

func human(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= mb:
		return fmt.Sprintf("%.0f MB", float64(n)/mb)
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
