package main

import (
	"embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sync"
	"time"
)

// version is set at build time via -ldflags="-X main.version=..."
var version = "dev"

var importerMu sync.Mutex
var importerRunning bool

//go:embed index.html.tmpl
var tmplFS embed.FS
var tmpl = template.Must(
	template.New("index.html.tmpl").
		Funcs(template.FuncMap{
			// duration formats the elapsed time between two timestamps.
			"duration": func(start, end time.Time) string {
				if end.IsZero() {
					return ""
				}
				d := end.Sub(start).Round(time.Second)
				if d < time.Minute {
					return d.String()
				}
				return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
			},
			// not is needed because Go templates have no built-in boolean negation.
			"not": func(b bool) bool { return !b },
			// stepCell renders a uniform step status cell.
			// fatalStep is AlbumResult.FatalStep; when it matches the step's key
			// the cell is marked fatal rather than a warning.
			"stepCell": func(label string, s StepStatus, fatalStep string) template.HTML {
				var statusClass, statusText, errHTML string
				switch {
				case s.Err != nil && fatalStep != "" && stepKey(label) == fatalStep:
					statusClass = "step-fatal"
					statusText = "✗ fatal"
					errHTML = `<span class="step-err">` + template.HTMLEscapeString(s.Err.Error()) + `</span>`
				case s.Err != nil:
					statusClass = "step-warn"
					statusText = "⚠ error"
					errHTML = `<span class="step-err">` + template.HTMLEscapeString(s.Err.Error()) + `</span>`
				case s.Skipped:
					statusClass = "step-warn"
					statusText = "– skipped"
				default:
					statusClass = "step-ok"
					statusText = "✓ ok"
				}
				return template.HTML(`<div class="step">` +
					`<span class="step-label">` + template.HTMLEscapeString(label) + `</span>` +
					`<span class="` + statusClass + `">` + statusText + `</span>` +
					errHTML +
					`</div>`)
			},
		}).
		ParseFS(tmplFS, "index.html.tmpl"),
)

// stepKey maps a human-readable step label to the FatalStep identifier used in
// AlbumResult so the template can highlight the step that caused the abort.
func stepKey(label string) string {
	switch label {
	case "Metadata":
		return "TagMetadata"
	case "Cover Art":
		return "CoverArt"
	default:
		return label
	}
}

type templateData struct {
	Running bool
	Version string
	Session *ImportSession
}

func handleHome(w http.ResponseWriter, r *http.Request) {
	importerMu.Lock()
	running := importerRunning
	importerMu.Unlock()

	if err := tmpl.Execute(w, templateData{
		Running: running,
		Version: version,
		Session: lastSession,
	}); err != nil {
		log.Println("Template error:", err)
	}
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
