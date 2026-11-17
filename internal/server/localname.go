package server

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// LocalName is the friendly address of the dashboard on this Mac: http://colima.localhost:7766.
// Names under .localhost never leave the machine (RFC 6761). Chrome, Edge and Firefox resolve
// them by themselves; Safari and command-line tools need a line in /etc/hosts, which install.sh
// adds and the "localname" check offers to add.
const LocalName = "colima.localhost"

// HostsFile is where macOS keeps static names; a variable so tests can point it elsewhere.
var HostsFile = "/etc/hosts"

// HostsLine is the line install.sh and the fix button add.
const HostsLine = "127.0.0.1 " + LocalName

// HasLocalName reports whether the hosts file maps LocalName to 127.0.0.1.
func HasLocalName() bool {
	f, err := os.Open(HostsFile)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "127.0.0.1" {
			continue
		}
		for _, name := range fields[1:] {
			if strings.EqualFold(name, LocalName) {
				return true
			}
		}
	}
	return false
}

// BaseURL is the address to show and open: the friendly name when this Mac resolves it,
// else the plain loopback address.
func BaseURL(port int) string {
	host := "127.0.0.1"
	if HasLocalName() {
		host = LocalName
	}
	return "http://" + host + ":" + strconv.Itoa(port)
}

func (s *Server) localNameCheck() Check {
	c := Check{ID: "localname", Title: "Easy address " + LocalName, State: "pass", Detail: "open " + BaseURL(s.opt.Port) + " in any browser"}
	if HasLocalName() {
		return c
	}
	c.State = "warn"
	c.Detail = "Safari and command-line tools cannot find " + LocalName + " yet; this adds one line to /etc/hosts (asks for your Mac password)"
	return inTerminal(c, "grep -q '"+LocalName+"' /etc/hosts || echo '"+HostsLine+"' | sudo tee -a /etc/hosts >/dev/null; sudo dscacheutil -flushcache; sudo killall -HUP mDNSResponder")
}
