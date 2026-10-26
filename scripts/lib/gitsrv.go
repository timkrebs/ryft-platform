// A smart-HTTP git server for the kind run (scripts/lib/admission_full.sh): `git http-backend` as a CGI
// behind net/http, serving the bare repositories under /srv read-only. Argo CD reads the tree under test
// from it, from inside the cluster, so a sync deploys exactly the tree the run was given. Built by the
// script (go build, linux, the node's architecture) into an Alpine image with git and git-daemon (which
// ships http-backend); nothing here
// ships anywhere.
package main

import (
	"log"
	"net/http"
	"net/http/cgi"
)

func main() {
	h := &cgi.Handler{
		Path: "/usr/bin/git",
		Args: []string{"http-backend"},
		Env: []string{
			"GIT_PROJECT_ROOT=/srv",
			"GIT_HTTP_EXPORT_ALL=1",
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=safe.directory",
			"GIT_CONFIG_VALUE_0=*",
			"HOME=/tmp",
		},
	}
	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	http.Handle("/", h)
	log.Fatal(http.ListenAndServe(":8080", nil))
}
