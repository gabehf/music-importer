package main

import (
	"log"
	"net/http"
	"sync"
	"text/template"
)

// version is set at build time via -ldflags="-X main.version=..."
var version = "dev"

var importerMu sync.Mutex
var importerRunning bool

var tmpl = template.Must(template.New("index").Parse(`
<!DOCTYPE html>
<html>
<head>
	<title>Music Importer</title>
	<style>
		body {
			font-family: sans-serif;
			background: #111;
			color: #eee;
			text-align: center;
			padding-top: 80px;
		}
		button {
			font-size: 32px;
			padding: 20px 40px;
			border-radius: 10px;
			border: none;
			cursor: pointer;
			background: #4CAF50;
			color: white;
		}
		button:disabled {
			background: #555;
			cursor: not-allowed;
		}
		footer {
			position: fixed;
			bottom: 16px;
			width: 100%;
			font-size: 13px;
			color: #999;
		}
	</style>
</head>
<body>
	<h1>Music Importer</h1>
	<form action="/run" method="POST">
		<button type="submit" {{if .Running}}disabled{{end}}>
			{{if .Running}}Importer Running...{{else}}Run Importer{{end}}
		</button>
	</form>
	<footer>{{.Version}}</footer>
</body>
</html>
`))

func handleHome(w http.ResponseWriter, r *http.Request) {
	importerMu.Lock()
	running := importerRunning
	importerMu.Unlock()

	tmpl.Execute(w, struct {
		Running bool
		Version string
	}{Running: running, Version: version})
}

func handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	importerMu.Lock()
	running := importerRunning
	importerMu.Unlock()

	if running {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	go RunImporter()

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func main() {
	log.Printf("Music Importer %s starting on http://localhost:8080", version)
	http.HandleFunc("/", handleHome)
	http.HandleFunc("/run", handleRun)

	log.Fatal(http.ListenAndServe(":8080", nil))
}
