package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/colima"
)

// Behind a company proxy that inspects TLS (Zscaler, Netskope and the like) the Mac trusts the
// proxy's root certificate through its keychain, but the Colima VM does not, so every pull fails
// with "x509: certificate signed by unknown authority". Rancher Desktop copies the Mac's
// certificates into its VM; this does the same, on request.

// certProbe is fetched from inside the VM to see whether TLS works there.
const certProbe = "https://registry-1.docker.io/v2/"

// vmCertDir is where the Mac's certificates go inside the VM, one file each, which is what
// update-ca-certificates expects.
const vmCertDir = "/usr/local/share/ca-certificates/colima-dashboard"

// macKeychains hold the certificates people and their IT department added; Apple's own roots
// live elsewhere and the VM already has the public ones.
func macKeychains() []string {
	return []string{"/Library/Keychains/System.keychain", filepath.Join(homeDir(), "Library", "Keychains", "login.keychain-db")}
}

// macCAs returns the certificate authorities in the Mac's keychains as one PEM bundle.
func macCAs(ctx context.Context) (bundle []byte, names []string, err error) {
	var all []byte
	for _, kc := range macKeychains() {
		out, err := exec.CommandContext(ctx, "/usr/bin/security", "find-certificate", "-a", "-p", kc).Output()
		if err == nil {
			all = append(all, out...)
		}
	}
	if len(all) == 0 {
		return nil, nil, errors.New("could not read the Mac's keychains")
	}
	bundle, names = caBundle(all, time.Now())
	return bundle, names, nil
}

// caBundle keeps the unexpired CA certificates from PEM input, once each.
func caBundle(in []byte, now time.Time) ([]byte, []string) {
	var out bytes.Buffer
	var names []string
	seen := map[[32]byte]bool{}
	for {
		var b *pem.Block
		b, in = pem.Decode(in)
		if b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil || !c.IsCA || now.After(c.NotAfter) {
			continue
		}
		sum := sha256.Sum256(b.Bytes)
		if seen[sum] {
			continue
		}
		seen[sum] = true
		_ = pem.Encode(&out, &pem.Block{Type: "CERTIFICATE", Bytes: b.Bytes})
		name := c.Subject.CommonName
		if name == "" {
			name = c.Subject.String()
		}
		names = append(names, name)
	}
	return out.Bytes(), names
}

// vmTLS fetches certProbe from inside the VM. It returns curl's exit code: 0 works, 60 means
// the server's certificate is not trusted there, anything else is a network problem.
func (s *Server) vmTLS(ctx context.Context, profile string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	// colima ssh turns every failure into exit 1, so curl's own code is printed instead.
	cmd, err := s.opt.Colima.SSHCommand(ctx, profile, "sh", "-c", `curl -sS -o /dev/null --max-time 8 "$1" 2>/dev/null; echo "curl=$?"`, "sh", certProbe)
	if err != nil {
		return -1, err
	}
	out, err := cmd.Output()
	if err != nil {
		return -1, fmt.Errorf("could not run curl in the VM: %v", err)
	}
	return parseCurlCode(string(out))
}

func parseCurlCode(out string) (int, error) {
	i := strings.LastIndex(out, "curl=")
	if i < 0 {
		return -1, errors.New("unexpected answer from the VM")
	}
	var code int
	if _, err := fmt.Sscanf(strings.TrimSpace(out[i+5:]), "%d", &code); err != nil {
		return -1, errors.New("unexpected answer from the VM")
	}
	return code, nil
}

// certPassed remembers, per profile, when TLS last worked in the VM: the sidebar badge runs the
// checks often, and a passing answer does not need an ssh round trip each time.
var certPassed sync.Map // profile -> time.Time

// certCheck is the diagnostics row.
func (s *Server) certCheck(ctx context.Context, profile string) Check {
	c := Check{ID: "certs", Title: "The VM trusts your network's certificates"}
	if t, ok := certPassed.Load(profile); ok && time.Since(t.(time.Time)) < 10*time.Minute {
		c.State, c.Detail = "pass", "HTTPS from inside the VM works (Docker Hub answered)"
		return c
	}
	code, err := s.vmTLS(ctx, profile)
	switch {
	case err != nil:
		c.State, c.Detail = "skip", err.Error()
	case code == 0:
		c.State, c.Detail = "pass", "HTTPS from inside the VM works (Docker Hub answered)"
		certPassed.Store(profile, time.Now())
	case code == 60:
		certPassed.Delete(profile)
		c.State = "fail"
		c.Detail = "Docker Hub's certificate is not trusted inside the VM, so pulls fail with x509 errors. Usually a company proxy that inspects TLS; your Mac trusts its certificate, the VM does not"
		if _, names, err := macCAs(ctx); err == nil && len(names) > 0 {
			c.Fix, c.Action = fmt.Sprintf("Copy %d certificates from your Mac's keychain into the VM", len(names)), "certs"
			c.Confirm = "Copies the certificate authorities in your Mac's keychains (" + summarize(names, 4) + ") into the VM, then restarts its Docker engine (and k3s): running containers restart."
		}
	default:
		c.State, c.Detail = "skip", fmt.Sprintf("could not reach Docker Hub from the VM (curl exit %d)", code)
	}
	return c
}

func summarize(names []string, n int) string {
	if len(names) <= n {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:n], ", ") + fmt.Sprintf(" and %d more", len(names)-n)
}

// vmCertScript installs the bundle on stdin and restarts what reads certificates at start.
const vmCertScript = `set -e
rm -rf "$1" /usr/local/share/ca-certificates/colima-dashboard-mac.crt
mkdir -p "$1"
awk -v dir="$1" '/BEGIN CERTIFICATE/ { n++; f = sprintf("%s/mac-%02d.crt", dir, n) } f { print > f } /END CERTIFICATE/ { close(f); f = "" }'
update-ca-certificates >/dev/null 2>&1
echo "installed $(ls "$1" | wc -l) certificates; restarting the container engine"
if systemctl is-active --quiet docker; then systemctl restart docker; fi
if systemctl is-active --quiet containerd && ! systemctl is-active --quiet docker; then systemctl restart containerd; fi
if systemctl is-active --quiet k3s; then echo "restarting k3s"; systemctl restart k3s; fi`

// POST /api/profiles/{p}/certs: copy the Mac's certificate authorities into the VM.
func (s *Server) apiCerts(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("p")
	if !colima.ValidProfile(p) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid profile name %q", p))
		return
	}
	bundle, names, err := macCAs(r.Context())
	if err != nil || len(names) == 0 {
		writeError(w, http.StatusUnprocessableEntity, errors.New("no certificate authorities found in the Mac's keychains"))
		return
	}
	if err := s.opt.Colima.Claim(p, "certificates"); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	job := s.jobs.Start("copy certificates ("+p+")", p, func(j *Job) error {
		defer s.opt.Colima.Release(p)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		j.Line(fmt.Sprintf("copying %d certificate authorities from the Mac's keychains:", len(names)))
		for _, n := range names {
			j.Line("  " + n)
		}
		cmd, err := s.opt.Colima.SSHCommand(ctx, p, "sudo", "sh", "-c", vmCertScript, "sh", vmCertDir)
		if err != nil {
			return err
		}
		cmd.Stdin = bytes.NewReader(bundle)
		cmd.Stdout, cmd.Stderr = j, j
		if err := cmd.Run(); err != nil {
			return err
		}
		time.Sleep(2 * time.Second)
		code, err := s.vmTLS(ctx, p)
		switch {
		case err != nil:
			return err
		case code == 0:
			j.Line("done: HTTPS from inside the VM works now")
			return nil
		case code == 60:
			return errors.New("still not trusted: the proxy's root certificate is not in the Mac's System or login keychain")
		default:
			j.Line(fmt.Sprintf("installed, but Docker Hub could not be reached from the VM to confirm (curl exit %d)", code))
			return nil
		}
	})
	writeJSON(w, http.StatusAccepted, job.snapshot())
}
