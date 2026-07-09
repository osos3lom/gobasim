package web

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
)

// TestPageTemplatesRenderDistinctContent guards against the content-block
// name collision bug: dashboard.html, logs.html, and workflow.html each
// used to define their body under the same shared template name
// ("content"), so whichever file the html/template glob parsed last won
// that name for every page rendered through layout.html. Each page now
// defines a uniquely-named content block, selected by layout.html via the
// "Page" field already passed into ExecuteTemplate by every handler.
func TestPageTemplatesRenderDistinctContent(t *testing.T) {
	tmpl := template.Must(template.New("layout").ParseFS(templatesFS, "templates/*.html"))

	cases := []struct {
		file    string
		data    map[string]interface{}
		want    string
		notWant []string
	}{
		{
			file: "dashboard.html",
			data: map[string]interface{}{
				"Page": "dashboard", "WAStatus": "disconnected",
			},
			want:    "Recent Chat Transactions",
			notWant: []string{"Live Event Logger", "Create a new agent", "WhatsApp Linking"},
		},
		{
			file:    "logs.html",
			data:    map[string]interface{}{"Page": "logs"},
			want:    "Live Event Logger",
			notWant: []string{"Recent Chat Transactions", "Create a new agent", "WhatsApp Linking"},
		},
		{
			file:    "workflow.html",
			data:    map[string]interface{}{"Page": "workflows"},
			want:    "Create a new agent",
			notWant: []string{"Recent Chat Transactions", "Live Event Logger", "WhatsApp Linking"},
		},
		{
			file: "whatsapp.html",
			data: map[string]interface{}{
				"Page": "whatsapp", "WAStatus": "disconnected",
			},
			want:    "WhatsApp Linking",
			notWant: []string{"Recent Chat Transactions", "Live Event Logger", "Create a new agent"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			var buf bytes.Buffer
			if err := tmpl.ExecuteTemplate(&buf, tc.file, tc.data); err != nil {
				t.Fatalf("ExecuteTemplate(%s) error: %v", tc.file, err)
			}
			out := buf.String()
			if !strings.Contains(out, tc.want) {
				t.Errorf("%s: expected output to contain %q, got:\n%s", tc.file, tc.want, out)
			}
			for _, nw := range tc.notWant {
				if strings.Contains(out, nw) {
					t.Errorf("%s: output unexpectedly contains %q (leaked from another page's content block)", tc.file, nw)
				}
			}
		})
	}
}
