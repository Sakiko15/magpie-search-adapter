package magpie

import (
	"strings"
	"testing"
)

func TestParsePages(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		want  []Result
		empty bool
	}{
		{
			name: "numbered pages",
			text: "1. Go downloads — https://go.dev/dl/\ngo1.27.1 is the latest release.\n\n2. Go blog — https://go.dev/blog/\nThe Go blog.\n",
			want: []Result{
				{Title: "Go downloads", URL: "https://go.dev/dl/", Content: "go1.27.1 is the latest release."},
				{Title: "Go blog", URL: "https://go.dev/blog/", Content: "The Go blog."},
			},
		},
		{
			name: "title with a dash",
			text: "1. Foo — Bar — https://example.com/a\ncontent\n",
			want: []Result{{Title: "Foo — Bar", URL: "https://example.com/a", Content: "content"}},
		},
		{
			name: "blank line inside content",
			text: "1. A — https://a.example/x\nfirst\n\nsecond\n\n2. B — https://b.example/y\nthird\n",
			want: []Result{
				{Title: "A", URL: "https://a.example/x", Content: "first\n\nsecond"},
				{Title: "B", URL: "https://b.example/y", Content: "third"},
			},
		},
		{
			name: "duplicate url keeps the first",
			text: "1. A — https://a.example/x\none\n\n2. A again — https://a.example/x\ntwo\n",
			want: []Result{{Title: "A", URL: "https://a.example/x", Content: "one"}},
		},
		{
			name: "prose fallback with markdown links",
			text: "Based on the results, see [Go downloads](https://go.dev/dl/) and https://go.dev/blog/.",
			want: []Result{
				{Title: "Go downloads", URL: "https://go.dev/dl/"},
				{Title: "go.dev", URL: "https://go.dev/blog/"},
			},
		},
		{
			name: "url with parentheses",
			text: "1. Go — https://en.wikipedia.org/wiki/Go_(programming_language)\ncontent\n",
			want: []Result{{Title: "Go", URL: "https://en.wikipedia.org/wiki/Go_(programming_language)", Content: "content"}},
		},
		{
			name:  "empty",
			text:  "",
			empty: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParsePages(tt.text)
			if tt.empty {
				if len(got) != 0 {
					t.Fatalf("got %#v, want none", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d results %#v, want %d", len(got), got, len(tt.want))
			}
			for i := range got {
				if got[i].Title != tt.want[i].Title || got[i].URL != tt.want[i].URL {
					t.Errorf("result %d = %#v, want %#v", i, got[i], tt.want[i])
				}
				if tt.want[i].Content != "" && got[i].Content != tt.want[i].Content {
					t.Errorf("result %d content = %q, want %q", i, got[i].Content, tt.want[i].Content)
				}
			}
		})
	}
}

func TestParseProseBullets(t *testing.T) {
	text := `Recent news regarding OpenAI includes:

* **$11 Billion Funding Round:** OpenAI secured roughly $11 billion.
  *Page:* [OpenAI raises $11bn | MEO](https://example.com/a)

* **Security Partnership:** OpenAI announced a partnership.
  *Page:* [OpenAI News](https://example.com/b)

Sources:
- example.com — https://example.com/a
- example.com — https://example.com/b`
	got := ParsePages(text)
	if len(got) != 2 {
		t.Fatalf("got %d results %#v", len(got), got)
	}
	if got[0].Title != "OpenAI raises $11bn | MEO" || got[0].URL != "https://example.com/a" {
		t.Errorf("result 0 = %#v", got[0])
	}
	if !strings.Contains(got[0].Content, "$11 Billion Funding Round") || strings.Contains(got[0].Content, "Security Partnership") {
		t.Errorf("result 0 content = %q", got[0].Content)
	}
	if strings.Contains(got[0].Content, "Page:") || strings.Contains(got[0].Content, "**") {
		t.Errorf("result 0 content kept decoration: %q", got[0].Content)
	}
	if got[1].Title != "OpenAI News" || got[1].URL != "https://example.com/b" {
		t.Errorf("result 1 = %#v", got[1])
	}
}

func TestParseProsePages(t *testing.T) {
	text := `Intro

* **Headline one:** summary one.
  *Page:* [Page one](https://example.com/1)

* **Headline two:** summary two.
  *Pages:*
  * [Page two](https://example.com/2)
  * [Page three](https://example.com/3)

* **Headline four:** summary four.
  *Page:* https://example.com/4

Sources:
- example.com — https://example.com/1`
	got := ParsePages(text)
	if len(got) != 4 {
		t.Fatalf("got %d results %#v", len(got), got)
	}
	wantTitles := []string{"Page one", "Page two", "Page three", "Headline four"}
	wantURLs := []string{"https://example.com/1", "https://example.com/2", "https://example.com/3", "https://example.com/4"}
	for i := range got {
		if got[i].Title != wantTitles[i] || got[i].URL != wantURLs[i] {
			t.Errorf("result %d = %#v, want title %q url %q", i, got[i], wantTitles[i], wantURLs[i])
		}
	}
	if !strings.Contains(got[2].Content, "summary two") || strings.Contains(got[2].Content, "Page two") {
		t.Errorf("result 2 content = %q", got[2].Content)
	}
}

func TestParseProseParenthesizedURL(t *testing.T) {
	text := "* **Go:** language.\n  *Page:* [Go (programming language)](https://en.wikipedia.org/wiki/Go_(programming_language))"
	got := ParsePages(text)
	if len(got) != 1 || got[0].URL != "https://en.wikipedia.org/wiki/Go_(programming_language)" {
		t.Fatalf("got %#v", got)
	}
	if got[0].Title != "Go (programming language)" {
		t.Fatalf("title = %q", got[0].Title)
	}
}

func TestParsePagesNoURLProse(t *testing.T) {
	got := ParsePages("Magpie found nothing with a URL, but it says hello.")
	if len(got) != 1 || got[0].URL != "" || got[0].Title != "Magpie web search" {
		t.Fatalf("got %#v", got)
	}
}

func TestParseFetchText(t *testing.T) {
	tests := []struct {
		name        string
		text        string
		wantTitle   string
		wantContent string
		wantErr     bool
	}{
		{
			name:        "with title",
			text:        "URL: https://example.com/x\nTitle: Example\n\nHello\nworld",
			wantTitle:   "Example",
			wantContent: "Hello\nworld",
		},
		{
			name:        "without title",
			text:        "URL: https://example.com/x\n\nHello",
			wantContent: "Hello",
		},
		{
			name:        "continuation marker",
			text:        "URL: https://example.com/x\nTitle: Example\n\nHello\n\n[10 more characters: call web_fetch with offset=5 to read on]",
			wantTitle:   "Example",
			wantContent: "Hello",
		},
		{
			name:        "no text",
			text:        "URL: https://example.com/x\n\n(the page has no text)",
			wantContent: "",
		},
		{
			name:    "missing url",
			text:    "Title: Example\n\nHello",
			wantErr: true,
		},
		{
			name:    "empty",
			text:    "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			title, content, err := ParseFetchText(tt.text)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if title != tt.wantTitle || content != tt.wantContent {
				t.Fatalf("got title=%q content=%q, want %q %q", title, content, tt.wantTitle, tt.wantContent)
			}
		})
	}
}

func TestCleanURL(t *testing.T) {
	for raw, want := range map[string]string{
		"https://example.com/x).": "https://example.com/x",
		"https://example.com/x]":  "https://example.com/x",
		"ftp://example.com/x":     "",
		"not a url":               "",
		"https://en.wikipedia.org/wiki/Go_(programming_language)":   "https://en.wikipedia.org/wiki/Go_(programming_language)",
		"https://en.wikipedia.org/wiki/Go_(programming_language)).": "https://en.wikipedia.org/wiki/Go_(programming_language)",
	} {
		if got := CleanURL(raw); got != want {
			t.Errorf("CleanURL(%q) = %q, want %q", raw, got, want)
		}
	}
}
