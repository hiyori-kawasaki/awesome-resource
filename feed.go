package main

import (
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	client = &http.Client{Timeout: 15 * time.Second}

	reTitle = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reMeta  = regexp.MustCompile(`(?is)<meta\s[^>]*>`)
	reLink  = regexp.MustCompile(`(?is)<link\s[^>]*>`)
	reAttr  = regexp.MustCompile(`(?is)([a-zA-Z:_-]+)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	reHTML  = regexp.MustCompile(`<[^>]+>`)

	reForum = regexp.MustCompile(`(?i)(forum|discourse|community|reddit\.com|news\.ycombinator|lobste\.rs|stackoverflow|stackexchange)`)
	reDocs  = regexp.MustCompile(`(?i)((^|[./])(docs|documentation|developer|developers)[./]|readthedocs|/docs(/|$))`)
	reNews  = regexp.MustCompile(`(?i)\b(newsletter|weekly|digest)\b`)
)

// vocab: tag -> patrón (en minúsculas) que se busca en títulos y categorías de los posts.
var vocab = map[string]*regexp.Regexp{}

func init() {
	src := map[string]string{
		"golang":       `golang|goroutines?|go 1\.\d+|go modules?`,
		"rust":         `rust|rustc|cargo|tokio`,
		"python":       `python|django|flask|fastapi|pandas|numpy`,
		"javascript":   `javascript|typescript|node\.?js|react|vue|svelte|angular|deno|webpack|vite`,
		"java":         `java|jvm|spring|kotlin|jdk`,
		"zig":          `zig`,
		"elixir":       `elixir|erlang|beam|phoenix`,
		"devops":       `devops|kubernetes|k8s|docker|terraform|helm|ci/cd|github actions|observability`,
		"cloud":        `aws|gcp|azure|cloudflare|serverless|lambda`,
		"databases":    `postgres(?:ql)?|mysql|sqlite|database|databases|sql|redis|mongodb|cassandra|dynamodb`,
		"security":     `security|vulnerabilit(?:y|ies)|cve|encryption|privacy|authentication|malware|zero trust`,
		"ai-ml":        `machine learning|deep learning|llms?|ai|neural networks?|gpt|ml`,
		"data":         `data pipelines?|spark|kafka|etl|analytics|data engineering|data platform`,
		"architecture": `microservices?|architecture|distributed systems?|scalability|system design`,
		"performance":  `performance|latency|optimi[sz]ation|benchmarks?|profiling`,
		"mobile":       `android|ios|swift|flutter|react native`,
		"frontend":     `css|html|frontend|front end|accessibility|web components`,
		"linux":        `linux|kernel|systemd|ebpf`,
	}
	for tag, p := range src {
		vocab[tag] = regexp.MustCompile(`\b(?:` + p + `)\b`)
	}
}

type post struct {
	Title string
	Cats  []string
}

type feed struct {
	URL         string
	Title       string
	Description string
	Posts       []post
}

// ---------- inspección de un site ----------

// inspect captura la página y su feed y arma la ficha completa:
// título, descripción, tipo (blog/foro/docs/...) y tags detectados por los posts.
// Devuelve error solo si no se pudo leer ni la página ni un feed.
func inspect(ctx context.Context, raw string) (Site, error) {
	s := Site{URL: raw, AddedAt: time.Now()}

	page, pageErr := get(ctx, raw)
	var metaTitle, metaDesc string
	if pageErr == nil {
		metaTitle, metaDesc = parseMeta(page)
	}

	// un artículo suelto se describe con su propia página, no con el feed del site
	if pageErr == nil && isArticle(page, raw) {
		s.Kind = "article"
		s.Title = metaTitle
		if s.Title == "" {
			s.Title = hostOf(raw)
		}
		s.Description = metaDesc
		s.Tags = tagsFromText(metaTitle + " " + metaDesc)
		return s, nil
	}

	var f *feed
	if pageErr == nil {
		if fu := discoverFeed(page, raw); fu != "" {
			f, _ = readFeed(ctx, fu)
		}
	}
	if f == nil {
		f = guessFeed(ctx, raw)
	}
	if pageErr != nil && f == nil {
		s.Title = hostOf(raw)
		return s, pageErr
	}

	title, desc := metaTitle, metaDesc
	if f != nil {
		s.Feed = f.URL
		s.Tags = detectTags(f.Posts)
		if t := cleanTitle(f.Title); t != "" {
			title = t
		}
		// la descripción del canal suele decir mejor qué hay en el blog que la meta de la home
		if d := clean(stripTags(html.UnescapeString(f.Description)), 220); len([]rune(d)) >= 20 && !strings.EqualFold(d, title) {
			desc = d
		}
	}
	if title == "" {
		title = hostOf(raw)
	}
	s.Title, s.Description = title, desc
	s.Kind = detectKind(raw, title, desc, f != nil)
	return s, nil
}

func detectKind(rawURL, title, desc string, hasFeed bool) string {
	switch {
	case reForum.MatchString(rawURL):
		return "forum"
	case reDocs.MatchString(rawURL):
		return "docs"
	case reNews.MatchString(title + " " + desc):
		return "newsletter"
	case hasFeed:
		return "blog"
	default:
		return "site"
	}
}

// detectTags cuenta en cuántos de los últimos posts aparece cada tema del vocabulario.
// Un tag entra si aparece en al menos 2 posts y en >=10% de ellos; se devuelven los 4 más frecuentes.
func detectTags(posts []post) []string {
	if len(posts) == 0 {
		return nil
	}
	if len(posts) > 50 {
		posts = posts[:50]
	}
	counts := map[string]int{}
	norm := strings.NewReplacer("-", " ", "_", " ")
	for _, p := range posts {
		hay := norm.Replace(strings.ToLower(p.Title + " " + strings.Join(p.Cats, " ")))
		for tag, re := range vocab {
			if re.MatchString(hay) {
				counts[tag]++
			}
		}
	}

	type tc struct {
		tag string
		n   int
	}
	var list []tc
	for t, n := range counts {
		if n >= 2 && float64(n)/float64(len(posts)) >= 0.10 {
			list = append(list, tc{t, n})
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		return list[i].tag < list[j].tag
	})
	if len(list) > 4 {
		list = list[:4]
	}
	out := make([]string, len(list))
	for i, x := range list {
		out[i] = x.tag
	}
	return out
}

// ---------- red ----------

func get(ctx context.Context, raw string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; awesome-dev/1.0)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/rss+xml,application/atom+xml,application/xml;q=0.9,*/*;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil && len(b) == 0 {
		return "", err
	}
	return string(b), nil
}

// ---------- feeds ----------

type rssDoc struct {
	Channel struct {
		Title       string `xml:"title"`
		Description string `xml:"description"`
		Items       []struct {
			Title      string   `xml:"title"`
			Categories []string `xml:"category"`
		} `xml:"item"`
	} `xml:"channel"`
}

type atomDoc struct {
	Title    string `xml:"title"`
	Subtitle string `xml:"subtitle"`
	Entries  []struct {
		Title      string `xml:"title"`
		Categories []struct {
			Term string `xml:"term,attr"`
		} `xml:"category"`
	} `xml:"entry"`
}

func decodeXML(body string, v any) error {
	d := xml.NewDecoder(strings.NewReader(body))
	d.Strict = false
	d.Entity = xml.HTMLEntity
	d.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	return d.Decode(v)
}

func parseFeed(body string) (*feed, error) {
	head := strings.ToLower(body[:min(len(body), 2000)])
	if !strings.Contains(head, "<rss") && !strings.Contains(head, "<feed") {
		return nil, fmt.Errorf("no es un feed")
	}

	var r rssDoc
	if err := decodeXML(body, &r); err == nil && (len(r.Channel.Items) > 0 || r.Channel.Title != "") {
		f := &feed{Title: r.Channel.Title, Description: r.Channel.Description}
		for _, it := range r.Channel.Items {
			f.Posts = append(f.Posts, post{Title: it.Title, Cats: it.Categories})
		}
		return f, nil
	}

	var a atomDoc
	if err := decodeXML(body, &a); err == nil && (len(a.Entries) > 0 || a.Title != "") {
		f := &feed{Title: a.Title, Description: a.Subtitle}
		for _, e := range a.Entries {
			cats := make([]string, 0, len(e.Categories))
			for _, c := range e.Categories {
				cats = append(cats, c.Term)
			}
			f.Posts = append(f.Posts, post{Title: e.Title, Cats: cats})
		}
		return f, nil
	}
	return nil, fmt.Errorf("feed no válido")
}

func readFeed(ctx context.Context, u string) (*feed, error) {
	body, err := get(ctx, u)
	if err != nil {
		return nil, err
	}
	f, err := parseFeed(body)
	if err != nil {
		return nil, err
	}
	f.URL = u
	return f, nil
}

// discoverFeed busca <link rel="alternate" type="application/rss+xml" href="..."> en el HTML.
func discoverFeed(page, base string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	for _, tag := range reLink.FindAllString(page, -1) {
		attrs := attrMap(tag)
		rel := strings.ToLower(attrs["rel"])
		typ := strings.ToLower(attrs["type"])
		if strings.Contains(rel, "alternate") && (strings.Contains(typ, "rss") || strings.Contains(typ, "atom")) && attrs["href"] != "" {
			if u, err := b.Parse(html.UnescapeString(attrs["href"])); err == nil {
				return u.String()
			}
		}
	}
	return ""
}

// guessFeed prueba rutas comunes cuando la página no anuncia su feed.
func guessFeed(ctx context.Context, raw string) *feed {
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	origin := u.Scheme + "://" + u.Host
	base := origin + strings.TrimSuffix(u.Path, "/")
	suffixes := []string{"/feed", "/rss", "/feed.xml", "/rss.xml", "/atom.xml", "/index.xml"}

	var cands []string
	if base != origin { // el site vive en un subpath (ej. proton.me/blog)
		for _, s := range suffixes {
			cands = append(cands, base+s)
		}
	}
	for _, s := range suffixes {
		cands = append(cands, origin+s)
	}
	for _, c := range cands {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		f, err := readFeed(cctx, c)
		cancel()
		if err == nil && f != nil {
			return f
		}
	}
	return nil
}

// ---------- metadata del HTML ----------

func attrMap(tag string) map[string]string {
	attrs := map[string]string{}
	for _, m := range reAttr.FindAllStringSubmatch(tag, -1) {
		v := m[2]
		if v == "" {
			v = m[3]
		}
		attrs[strings.ToLower(m[1])] = v
	}
	return attrs
}

func parseMeta(page string) (string, string) {
	meta := map[string]string{}
	for _, tag := range reMeta.FindAllString(page, -1) {
		attrs := attrMap(tag)
		key := strings.ToLower(attrs["property"])
		if key == "" {
			key = strings.ToLower(attrs["name"])
		}
		if key == "" || attrs["content"] == "" {
			continue
		}
		if _, ok := meta[key]; !ok {
			meta[key] = attrs["content"]
		}
	}

	title := first(meta["og:title"], meta["twitter:title"])
	if title == "" {
		if m := reTitle.FindStringSubmatch(page); m != nil {
			title = m[1]
		}
	}
	desc := first(meta["og:description"], meta["description"], meta["twitter:description"])
	return cleanTitle(title), clean(desc, 220)
}

// ---------- utilidades de texto ----------

func first(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func stripTags(s string) string {
	return reHTML.ReplaceAllString(s, " ")
}

func cleanTitle(s string) string {
	return strings.TrimSuffix(clean(s, 100), " - Medium")
}

// clean decodifica entidades HTML, colapsa espacios y recorta a max runas.
func clean(s string, max int) string {
	s = strings.Join(strings.Fields(html.UnescapeString(s)), " ")
	r := []rune(s)
	if len(r) > max {
		return strings.TrimSpace(string(r[:max-1])) + "…"
	}
	return s
}

// isArticle decide si la URL es un artículo suelto y no la portada de un site:
// og:type=article, fecha de publicación, o un slug largo al final del path (a-b-c).
func isArticle(page, raw string) bool {
	for _, tag := range reMeta.FindAllString(page, -1) {
		a := attrMap(tag)
		key := strings.ToLower(first(a["property"], a["name"]))
		val := strings.ToLower(a["content"])
		if (key == "og:type" && val == "article") || key == "article:published_time" {
			return true
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	last := segs[len(segs)-1]
	return strings.Count(last, "-") >= 2
}

// tagsFromText detecta temas en un texto corto (título + descripción de un artículo).
func tagsFromText(text string) []string {
	hay := strings.NewReplacer("-", " ", "_", " ").Replace(strings.ToLower(text))
	var out []string
	for tag, re := range vocab {
		if re.MatchString(hay) {
			out = append(out, tag)
		}
	}
	sort.Strings(out)
	if len(out) > 4 {
		out = out[:4]
	}
	return out
}