// A small HTTP service whose only job is to give the build benchmark something real to compile.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		sum := sha256.Sum256([]byte(r.URL.Path))
		_ = json.NewEncoder(w).Encode(map[string]string{
			"path": r.URL.Path,
			"hash": hex.EncodeToString(sum[:]),
			"time": time.Now().UTC().Format(time.RFC3339),
		})
	})
	addr := ":8080"
	if v := os.Getenv("ADDR"); v != "" {
		addr = v
	}
	log.Fatal(http.ListenAndServe(addr, nil))
}
