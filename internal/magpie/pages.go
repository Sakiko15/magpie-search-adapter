package magpie

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Magpie's web_search text is:
//
//  1. Title — https://example.com/page
//     The page's text, up to 1500 bytes.
//
//  2. ...
//
// The dash is U+2014; some searches may use U+2013.
var (
	headerNumber = regexp.MustCompile(`^(\d+)\.\s+(.+)$`)
	markdownLink = regexp.MustCompile(`\[([^\]]{1,300})\]\((https?://[^\s()]*(?:\([^\s()]*\)[^\s()]*)*)\)`)
	anyURL       = regexp.MustCompile(`https?://[^\s<>"'\[\]]+`)
	bulletLine   = regexp.MustCompile(`^(?:[*+-]|\d+[.)])\s+`)
	pageLine     = regexp.MustCompile(`^\*?Pages?:\*?`)
	boldText     = regexp.MustCompile(`\*\*(.+?)\*\*`)
	sourcesEnd   = regexp.MustCompile(`(?i)^sources?\s*:?\s*$`)
	fetchMore    = regexp.MustCompile(`\n*\[[0-9]+ more characters: call web_fetch with offset=[0-9]+ to read on\]\s*$`)
	fetchEmpty   = regexp.MustCompile(`^\(the page has [0-9]+ characters, none from offset [0-9]+\)$`)
)

// ParsePages turns Magpie's web_search text into structured results. When
// Magpie answered with a model's prose instead of the numbered pages list
// (a model searcher, not a search API), it falls back to the URLs in the
// text, or to the whole text as one result.
func ParsePages(text string) []Result {
	text = strings.TrimSpace(sanitizeText(strings.ReplaceAll(text, "\r\n", "\n")))
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	var parsed []Result
	var current *Result
	expected := 1
	flush := func() {
		if current == nil {
			return
		}
		current.Content = strings.TrimRight(current.Content, " \t\r\n")
		current.Title = strings.TrimSpace(current.Title)
		current.URL = CleanURL(current.URL)
		if current.URL != "" {
			parsed = append(parsed, *current)
		}
		current = nil
	}
	for _, line := range lines {
		n, title, u, ok := parseHeader(line)
		if ok && n == expected {
			flush()
			current = &Result{Title: title, URL: u}
			expected++
			continue
		}
		if current != nil {
			if current.Content != "" {
				current.Content += "\n"
			}
			current.Content += line
		}
	}
	flush()
	out := finalize(parsed)
	if len(out) == 0 {
		if prose := parseProse(text); len(prose) > 0 {
			return prose
		}
		return fallback(text)
	}
	return out
}

// parseProse turns a model-written answer into results. Magpie's model
// searchers answer with a summary per bullet and page links below it:
//
//   - **Headline:** summary
//     *Page:* [Page title](https://example.com/page)
//
//   - **Headline:** summary
//     *Pages:*
//
//   - [Page title](https://example.com/page)
func parseProse(text string) []Result {
	lines := strings.Split(text, "\n")
	minIndent := -1
	for _, line := range lines {
		if n, ok := bulletIndent(line); ok && (minIndent < 0 || n < minIndent) {
			minIndent = n
		}
	}
	if minIndent < 0 {
		return nil
	}

	var items [][]string
	var current []string
	flush := func() {
		if len(current) > 0 {
			items = append(items, current)
			current = nil
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if sourcesEnd.MatchString(trimmed) {
			flush()
			break
		}
		if n, ok := bulletIndent(line); ok && n == minIndent {
			flush()
			current = []string{trimmed}
			continue
		}
		if current != nil && trimmed != "" {
			current = append(current, trimmed)
		}
	}
	flush()

	seen := make(map[string]bool)
	out := make([]Result, 0, len(items))
	for _, item := range items {
		links := proseLinks(item)
		if len(links) == 0 {
			continue
		}
		content := proseContent(item, proseTitle(item, links[0][1]))
		for _, link := range links {
			title, u := link[0], link[1]
			if u == "" || seen[u] {
				continue
			}
			seen[u] = true
			if title == "" {
				title = proseTitle(item, u)
			}
			out = append(out, Result{
				Title:   title,
				URL:     u,
				Content: content,
				Snippet: Snippet(content, 200),
				Score:   scoreFor(len(out)),
			})
		}
	}
	return out
}

// bulletIndent reports the indentation of a bullet line, so nested bullets
// under "Pages:" stay part of their parent item.
func bulletIndent(line string) (int, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if !bulletLine.MatchString(trimmed) {
		return 0, false
	}
	return len(line) - len(trimmed), true
}

// proseLinks collects an item's page links: every link on a "Page:" /
// "Pages:" line and on the lines right after it. Without a page marker it
// keeps the first link.
func proseLinks(lines []string) [][2]string {
	var out [][2]string
	add := func(title, u string) {
		u = CleanURL(u)
		if u == "" {
			return
		}
		for _, link := range out {
			if link[1] == u {
				return
			}
		}
		out = append(out, [2]string{strings.TrimSpace(title), u})
	}
	inPages := false
	for _, line := range lines {
		if pageLine.MatchString(line) {
			inPages = true
			addLinks(line, add)
			continue
		}
		if inPages {
			if linkOnlyLine(line) {
				addLinks(line, add)
				continue
			}
			inPages = false
		}
	}
	if len(out) > 0 {
		return out
	}
	for _, line := range lines {
		if m := markdownLink.FindStringSubmatch(line); m != nil {
			add(m[1], m[2])
			break
		}
	}
	if len(out) > 0 {
		return out
	}
	for _, line := range lines {
		if m := anyURL.FindString(line); m != "" {
			add("", m)
			break
		}
	}
	return out
}

func addLinks(line string, add func(title, u string)) {
	for _, m := range markdownLink.FindAllStringSubmatch(line, -1) {
		add(m[1], m[2])
	}
	for _, u := range anyURL.FindAllString(line, -1) {
		add("", u)
	}
}

// linkOnlyLine reports whether line holds only a link, as in a "Pages:"
// entry.
func linkOnlyLine(line string) bool {
	s := bulletLine.ReplaceAllString(strings.TrimSpace(line), "")
	if s == "" {
		return false
	}
	s = markdownLink.ReplaceAllString(s, "")
	s = anyURL.ReplaceAllString(s, "")
	s = strings.TrimSpace(strings.Trim(s, "—–-"))
	return s == ""
}

// proseTitle uses the item's headline when the link itself has no title.
func proseTitle(lines []string, u string) string {
	for _, line := range lines {
		if m := boldText.FindStringSubmatch(line); m != nil {
			if title := strings.TrimSpace(strings.TrimRight(m[1], ":")); title != "" {
				return title
			}
		}
	}
	return HostOf(u)
}

// proseContent keeps the item's summary text, dropping page links and
// Markdown decoration.
func proseContent(lines []string, title string) string {
	kept := make([]string, 0, len(lines))
	inPages := false
	for _, line := range lines {
		if pageLine.MatchString(line) {
			inPages = true
			continue
		}
		if inPages {
			if linkOnlyLine(line) {
				continue
			}
			inPages = false
		}
		kept = append(kept, line)
	}
	content := strings.Join(kept, "\n")
	content = bulletLine.ReplaceAllString(content, "")
	content = strings.ReplaceAll(content, "**", "")
	content = markdownLink.ReplaceAllString(content, "$1")
	content = Snippet(content, 1200)
	if content == "" {
		content = title
	}
	return content
}

// parseHeader reads one "N. Title — URL" line, using the last dash before
// the URL so a title containing " — " still works.
func parseHeader(line string) (int, string, string, bool) {
	m := headerNumber.FindStringSubmatch(line)
	if m == nil {
		return 0, "", "", false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, "", "", false
	}
	rest := strings.TrimSpace(m[2])
	idx, sep := lastSeparator(rest)
	if idx < 0 {
		return 0, "", "", false
	}
	title := strings.TrimSpace(rest[:idx])
	u := CleanURL(strings.TrimSpace(rest[idx+len(sep):]))
	if u == "" {
		return 0, "", "", false
	}
	return n, title, u, true
}

func lastSeparator(s string) (int, string) {
	best, sep := -1, ""
	for _, p := range []string{" — ", " – "} {
		if i := strings.LastIndex(s, p); i > best {
			best, sep = i, p
		}
	}
	return best, sep
}

func finalize(parsed []Result) []Result {
	index := make(map[string]int, len(parsed))
	out := make([]Result, 0, len(parsed))
	for _, r := range parsed {
		if r.URL == "" {
			continue
		}
		if i, ok := index[r.URL]; ok {
			if out[i].Content == "" {
				out[i].Content = strings.TrimSpace(r.Content)
			}
			continue
		}
		r.Title = strings.TrimSpace(r.Title)
		if r.Title == "" {
			r.Title = r.URL
		}
		r.Content = strings.TrimSpace(r.Content)
		if r.Snippet == "" {
			r.Snippet = Snippet(r.Content, 160)
		}
		if r.Score <= 0 {
			r.Score = scoreFor(len(out))
		}
		index[r.URL] = len(out)
		out = append(out, r)
	}
	return out
}

func fallback(text string) []Result {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	urls, titles := looseURLs(text)
	if len(urls) == 0 {
		return []Result{{
			Title:   "Magpie web search",
			Content: text,
			Snippet: Snippet(text, 160),
			Score:   0.5,
		}}
	}
	out := make([]Result, 0, len(urls))
	for i, u := range urls {
		title := strings.TrimSpace(titles[u])
		if title == "" {
			title = HostOf(u)
		}
		out = append(out, Result{
			Title:   title,
			URL:     u,
			Content: text,
			Snippet: Snippet(text, 160),
			Score:   scoreFor(i),
		})
	}
	return out
}

// looseURLs finds Markdown links and bare URLs, keeping order and the link
// text as a title when there is one.
func looseURLs(text string) ([]string, map[string]string) {
	titles := make(map[string]string)
	var urls []string
	seen := make(map[string]bool)
	add := func(u, title string) {
		u = CleanURL(u)
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		urls = append(urls, u)
		if title != "" {
			titles[u] = strings.TrimSpace(title)
		}
	}
	for _, m := range markdownLink.FindAllStringSubmatch(text, -1) {
		add(m[2], m[1])
		if len(urls) == 20 {
			break
		}
	}
	for _, m := range anyURL.FindAllString(text, -1) {
		add(m, "")
		if len(urls) == 20 {
			break
		}
	}
	return urls, titles
}

// CleanURL trims the punctuation a Markdown link or sentence leaves on a
// URL, and checks that it is an http(s) URL with a host. A closing bracket
// is kept when the URL itself contains its opening counterpart, so
// .../Go_(programming_language) survives while "see https://x/y)" loses
// only the sentence's bracket.
func CleanURL(raw string) string {
	u := strings.TrimSpace(raw)
	for len(u) > 0 {
		switch u[len(u)-1] {
		case ')':
			if strings.Count(u, ")") <= strings.Count(u, "(") {
				return validURL(u)
			}
			u = u[:len(u)-1]
		case ']':
			if strings.Count(u, "]") <= strings.Count(u, "[") {
				return validURL(u)
			}
			u = u[:len(u)-1]
		case '.', ',', ';', ':', '!', '?', '}', '>', '"', '\'':
			u = u[:len(u)-1]
		default:
			return validURL(u)
		}
	}
	return ""
}

func validURL(u string) string {
	if u == "" {
		return ""
	}
	parsed, err := url.Parse(u)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ""
	}
	return u
}

// HostOf is a URL's host without a leading www, "" when it has none.
func HostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}

// Snippet is text's first max runes with its whitespace collapsed.
func Snippet(text string, max int) string {
	return TruncateRunes(strings.Join(strings.Fields(sanitizeText(text)), " "), max)
}

// TruncateRunes cuts s to at most max runes, with an ellipsis when it cut.
func TruncateRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + "…"
}

// ParseFetchText reads Magpie's web_fetch text:
//
//	URL: <final>
//	Title: <title>        (omitted when empty)
//
//	<Markdown text>
//
// and strips the continuation marker.
func ParseFetchText(text string) (title, content string, err error) {
	text = strings.TrimSpace(sanitizeText(strings.ReplaceAll(text, "\r\n", "\n")))
	if text == "" {
		return "", "", fmt.Errorf("empty web_fetch response")
	}
	header, body, ok := strings.Cut(text, "\n\n")
	if !ok {
		return "", "", fmt.Errorf("web_fetch response has no header")
	}
	var finalURL string
	for _, line := range strings.Split(header, "\n") {
		switch {
		case strings.HasPrefix(line, "URL: "):
			finalURL = strings.TrimSpace(strings.TrimPrefix(line, "URL: "))
		case strings.HasPrefix(line, "Title: "):
			title = strings.TrimSpace(strings.TrimPrefix(line, "Title: "))
		}
	}
	if finalURL == "" {
		return "", "", fmt.Errorf("web_fetch response has no URL")
	}
	body = strings.TrimSpace(fetchMore.ReplaceAllString(body, ""))
	switch {
	case body == "(the page has no text)":
		body = ""
	case fetchEmpty.MatchString(body):
		body = ""
	}
	return title, body, nil
}

func sanitizeText(s string) string {
	s = strings.ToValidUTF8(s, "�")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 0x20:
			return -1
		default:
			return r
		}
	}, s)
}

func scoreFor(i int) float64 {
	s := 1.0 - 0.05*float64(i)
	if s < 0.05 {
		return 0.05
	}
	return s
}
