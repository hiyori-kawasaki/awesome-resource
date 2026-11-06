// awesome-dev: captura metadata de sites tech, detecta sus temas por el feed
// y genera READMEs por tema.
//
//	awesome-dev add <tema|auto> [url...]  (sin URLs lee stdin)
//	awesome-dev detect <url>              (muestra lo detectado, no guarda)
//	awesome-dev refresh [tema...]         (vuelve a leer la metadata)
//	awesome-dev build                     (regenera los README)
//	awesome-dev push [mensaje]            (git add + commit + push)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const topicsDir = "topics"

type Site struct {
	URL         string    `json:"url"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Kind        string    `json:"kind,omitempty"` // blog, forum, docs, newsletter, site
	Tags        []string  `json:"tags,omitempty"`
	Feed        string    `json:"feed,omitempty"`
	Locked      bool      `json:"locked,omitempty"` // true = refresh no lo toca (edición manual)
	AddedAt     time.Time `json:"added_at"`
}

var reSlug = regexp.MustCompile(`[^a-z0-9]+`)

// nombres para mostrar de los temas que no quedan bien solo con mayúscula inicial
var names = map[string]string{
	"golang":     "Go",
	"ai-ml":      "AI & ML",
	"devops":     "DevOps",
	"javascript": "JavaScript",
}

func usage() {
	fmt.Fprintln(os.Stderr, `uso:
  awesome-dev add <tema|auto> [url...]  agrega sites (sin URLs lee stdin)
                                        "auto" los reparte en los temas detectados
  awesome-dev detect <url>              muestra tipo, tags y descripción (no guarda)
  awesome-dev refresh [tema...]         re-captura la metadata
  awesome-dev build                     regenera los README
  awesome-dev push [mensaje]            git add + commit + push`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "add":
		err = cmdAdd(args)
	case "detect":
		err = cmdDetect(args)
	case "refresh":
		err = cmdRefresh(args)
	case "build":
		err = build()
	case "push":
		err = cmdPush(args)
	default:
		usage()
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// ---------- comandos ----------

func cmdAdd(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("uso: awesome-dev add <tema|auto> [url...]")
	}
	slug := slugify(args[0])
	if slug == "" {
		return fmt.Errorf("tema inválido: %q", args[0])
	}
	raws := args[1:]
	if len(raws) == 0 {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		raws = strings.Fields(string(b))
	}

	// normaliza y deduplica el lote
	batch := map[string]bool{}
	var urls []string
	for _, r := range raws {
		u, err := normalize(r)
		if err != nil {
			fmt.Fprintln(os.Stderr, "  !", err)
			continue
		}
		if !batch[u] {
			batch[u] = true
			urls = append(urls, u)
		}
	}

	if slug == "auto" {
		return addAuto(urls)
	}

	sites, err := load(slug)
	if err != nil {
		return err
	}
	var fresh []string
	for _, u := range urls {
		if !hasURL(sites, u) {
			fresh = append(fresh, u)
		}
	}
	if len(fresh) == 0 {
		fmt.Println("nada nuevo que agregar")
		return nil
	}
	for _, r := range enrich(fresh) {
		sites = append(sites, r.Site)
	}
	if err := save(slug, sites); err != nil {
		return err
	}
	return build()
}

// addAuto agrega cada site a la carpeta de cada tema detectado (o a "general" si no hay).
func addAuto(urls []string) error {
	if len(urls) == 0 {
		fmt.Println("nada que agregar")
		return nil
	}
	for _, r := range enrich(urls) {
		targets := r.Site.Tags
		if len(targets) == 0 {
			targets = []string{"general"}
		}
		for _, t := range targets {
			sites, err := load(t)
			if err != nil {
				return err
			}
			if hasURL(sites, r.Site.URL) {
				continue
			}
			if err := save(t, append(sites, r.Site)); err != nil {
				return err
			}
		}
		fmt.Printf("    → %s\n", strings.Join(targets, ", "))
	}
	return build()
}

func cmdDetect(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("uso: awesome-dev detect <url>")
	}
	u, err := normalize(args[0])
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	s, err := inspect(ctx, u)
	if err != nil {
		return err
	}
	fmt.Printf("título:      %s\ntipo:        %s\nfeed:        %s\ntags:        %s\ndescripción: %s\n",
		s.Title, s.Kind, s.Feed, strings.Join(s.Tags, ", "), s.Description)
	return nil
}

func cmdRefresh(args []string) error {
	slugs := make([]string, 0, len(args))
	for _, a := range args {
		slugs = append(slugs, slugify(a))
	}
	if len(slugs) == 0 {
		var err error
		if slugs, err = topics(); err != nil {
			return err
		}
	}
	for _, slug := range slugs {
		old, err := load(slug)
		if err != nil {
			return err
		}
		fmt.Printf("[%s] %d sites\n", slug, len(old))

		// los marcados "locked" no se re-capturan
		var idx []int
		var urls []string
		for i, s := range old {
			if !s.Locked {
				idx = append(idx, i)
				urls = append(urls, s.URL)
			}
		}

		out := make([]Site, len(old))
		copy(out, old)
		for k, r := range enrich(urls) {
			i := idx[k]
			if r.Err != nil {
				continue // falló la captura: se conserva lo anterior
			}
			n := r.Site
			n.AddedAt = old[i].AddedAt
			if n.Feed == "" { // sin feed no hay tags nuevos: conserva los anteriores
				n.Tags = old[i].Tags
				if old[i].Kind != "" {
					n.Kind = old[i].Kind
				}
				if n.Description == "" {
					n.Description = old[i].Description
				}
			}
			out[i] = n
		}
		if err := save(slug, out); err != nil {
			return err
		}
	}
	return build()
}

func cmdPush(args []string) error {
	msg := "update: " + time.Now().Format("2006-01-02")
	if len(args) > 0 {
		msg = strings.Join(args, " ")
	}
	if err := git("add", "-A"); err != nil {
		return err
	}
	// exit 0 = no hay nada staged
	if exec.Command("git", "diff", "--cached", "--quiet").Run() == nil {
		fmt.Println("sin cambios, nada que subir")
		return nil
	}
	if err := git("commit", "-m", msg); err != nil {
		return err
	}
	return git("push")
}

func git(args ...string) error {
	c := exec.Command("git", args...)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("git %s: %w", args[0], err)
	}
	return nil
}

// ---------- captura en paralelo ----------

type result struct {
	Site Site
	Err  error
}

// enrich inspecciona todas las URLs en paralelo (máx. 8 a la vez),
// devolviendo los resultados en el mismo orden que la entrada.
func enrich(urls []string) []result {
	out := make([]result, len(urls))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			s, err := inspect(ctx, u)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ! %s: %v\n", u, err)
			} else {
				fmt.Printf("  ✓ %s [%s] %s\n", s.Title, s.Kind, strings.Join(s.Tags, ","))
			}
			out[i] = result{Site: s, Err: err}
		}(i, u)
	}
	wg.Wait()
	return out
}

// ---------- URLs ----------

func normalize(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("URL inválida: %q", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("solo http/https: %q", raw)
	}
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	q := u.Query()
	for k := range q {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "utm_") || lk == "fbclid" || lk == "gclid" || lk == "ref" || lk == "source" {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	u.Path = strings.TrimSuffix(u.Path, "/")
	return u.String(), nil
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return strings.TrimPrefix(u.Host, "www.")
}

// ---------- almacenamiento ----------

func slugify(s string) string {
	return strings.Trim(reSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

func display(slug string) string {
	if n, ok := names[slug]; ok {
		return n
	}
	s := strings.ReplaceAll(slug, "-", " ")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func hasURL(sites []Site, u string) bool {
	for _, s := range sites {
		if s.URL == u {
			return true
		}
	}
	return false
}

func topics() ([]string, error) {
	entries, err := os.ReadDir(topicsDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

func load(slug string) ([]Site, error) {
	b, err := os.ReadFile(filepath.Join(topicsDir, slug, "sites.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s []Site
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s/sites.json: %w", slug, err)
	}
	return s, nil
}

func save(slug string, sites []Site) error {
	dir := filepath.Join(topicsDir, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(sites, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "sites.json"), append(b, '\n'), 0o644)
}

// ---------- READMEs ----------

func esc(s string) string {
	return strings.NewReplacer("[", `\[`, "]", `\]`).Replace(s)
}

// line arma la entrada: **[Título](url)** — descripción · _tipo_ `tag` `tag`
func line(s Site, slug string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- **[%s](%s)**", esc(s.Title), s.URL)
	if s.Description != "" {
		fmt.Fprintf(&b, " — %s", s.Description)
	}
	var meta []string
	if s.Kind != "" {
		meta = append(meta, "_"+s.Kind+"_")
	}
	for _, t := range s.Tags {
		if t != slug { // no repetir el tema de la propia carpeta
			meta = append(meta, "`"+t+"`")
		}
	}
	if len(meta) > 0 {
		b.WriteString(" · " + strings.Join(meta, " "))
	}
	return b.String()
}

type node struct {
	key      string
	head     string
	children []string
}

// nodes agrupa por site: los artículos se anidan bajo la entrada de su site.
// Si hay varios artículos de un site sin su portada guardada, se crea un encabezado con el host.
func nodes(sites []Site, slug string) []node {
	byHost := map[string][]Site{}
	for _, s := range sites {
		h := hostOf(s.URL)
		byHost[h] = append(byHost[h], s)
	}

	var out []node
	for h, g := range byHost {
		var parent *Site
		var arts []Site
		for i := range g {
			if parent == nil && g[i].Kind != "article" {
				parent = &g[i]
			} else {
				arts = append(arts, g[i])
			}
		}
		sort.Slice(arts, func(i, j int) bool {
			return strings.ToLower(arts[i].Title) < strings.ToLower(arts[j].Title)
		})
		children := make([]string, len(arts))
		for i, a := range arts {
			children[i] = "  " + line(a, slug)
		}

		switch {
		case parent != nil:
			out = append(out, node{key: parent.Title, head: line(*parent, slug), children: children})
		case len(arts) == 1:
			out = append(out, node{key: arts[0].Title, head: line(arts[0], slug)})
		default:
			out = append(out, node{key: h, head: "- **" + h + "**", children: children})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		ki, kj := strings.ToLower(out[i].key), strings.ToLower(out[j].key)
		if ki != kj {
			return ki < kj
		}
		return out[i].head < out[j].head
	})
	return out
}

// build regenera topics/<tema>/README.md y el README.md de la raíz.
// La salida es determinista (sin fechas) para no generar commits vacíos.
func build() error {
	slugs, err := topics()
	if err != nil {
		return err
	}

	var rows strings.Builder
	total := 0
	for _, slug := range slugs {
		sites, err := load(slug)
		if err != nil {
			return err
		}
		sort.Slice(sites, func(i, j int) bool {
			return strings.ToLower(sites[i].Title) < strings.ToLower(sites[j].Title)
		})

		var b strings.Builder
		fmt.Fprintf(&b, "# %s\n\n> %d sitios\n\n", display(slug), len(sites))
		for _, n := range nodes(sites, slug) {
			b.WriteString(n.head + "\n")
			for _, c := range n.children {
				b.WriteString(c + "\n")
			}
		}
		b.WriteString("\n[← Volver al índice](../../README.md)\n")

		path := filepath.Join(topicsDir, slug, "README.md")
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(&rows, "| [%s](%s/%s/README.md) | %d |\n", display(slug), topicsDir, slug, len(sites))
		total += len(sites)
	}

	var root strings.Builder
	root.WriteString("# Awesome Software Development\n\n")
	root.WriteString("> Blogs y sites de tecnología, agrupados por tema.\n\n")
	fmt.Fprintf(&root, "%d temas · %d sitios\n\n", len(slugs), total)
	root.WriteString("## Temas\n\n| Tema | Sitios |\n|---|---|\n")
	root.WriteString(rows.String())
	return os.WriteFile("README.md", []byte(root.String()), 0o644)
}