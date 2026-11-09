// Smoke test for orcinus/base: identity, TLS against the system CA bundle,
// time zones, and that no shell exists.
package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"
)

func main() {
	fail := false
	check := func(name string, err error) {
		if err != nil {
			fail = true
			fmt.Println("FAIL", name+":", err)
		} else {
			fmt.Println("ok  ", name)
		}
	}
	if os.Getuid() == 0 {
		check("non-root", fmt.Errorf("running as root"))
	} else {
		check(fmt.Sprintf("non-root (uid %d)", os.Getuid()), nil)
	}
	resp, err := http.Get("https://github.com")
	if err == nil {
		resp.Body.Close()
	}
	check("tls with system CAs", err)
	_, err = time.LoadLocation("Asia/Jakarta")
	check("tzdata", err)
	if _, err := exec.LookPath("sh"); err == nil {
		check("no shell", fmt.Errorf("sh found"))
	} else {
		check("no shell", nil)
	}
	if err := os.WriteFile("/tmp/x", []byte("x"), 0o600); err != nil {
		check("writable /tmp", err)
	}
	if fail {
		os.Exit(1)
	}
}
