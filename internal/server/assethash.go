package server

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"sync"
)

var assetHash struct {
	once sync.Once
	sum  string
}

// assetHash identifies this build of the page. The page compares it with the one it loaded
// with, so an open tab picks up a rebuilt or updated dashboard instead of running old code.
func (s *Server) assetHash() string {
	assetHash.once.Do(func() {
		if s.opt.Assets == nil {
			return
		}
		h := sha256.New()
		_ = fs.WalkDir(s.opt.Assets, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := fs.ReadFile(s.opt.Assets, p)
			if err == nil {
				h.Write([]byte(p))
				h.Write(b)
			}
			return nil
		})
		assetHash.sum = hex.EncodeToString(h.Sum(nil))[:16]
	})
	return assetHash.sum
}
