// A tiny web app for the signed-app example: built static, run on orcinus/base.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	host, _ := os.Hostname()
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "hello from a signed image (pod %s, uid %d)\n", host, os.Getuid())
	})
	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
