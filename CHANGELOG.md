# Changelog

## v0.3.1: fix de refresh
- Corregido: `refresh` devolvía el tipo viejo (`blog`) a los artículos, porque no tienen feed.
  Ahora un artículo conserva `kind: article`.
  Cambio en `main.go`: `if n.Feed == "" && n.Kind != "article"`.

## v0.3: artículos y agrupado
- Corregido: un artículo suelto (ej. `proton.me/blog/ai-chatbot-survey`) salía con el título
  y la descripción del feed del site. Ahora usa los de su propia página.
- Nuevo `isArticle()`: detecta artículo por `og:type=article`, fecha de publicación
  o un slug largo al final de la URL (`a-b-c`).
- Nuevo `tagsFromText()`: los tags de un artículo salen de su título y descripción.
- README por tema: los artículos se anidan bajo su site.
  - Con la portada guardada: el artículo va debajo de ella.
  - Sin portada y con 2+ artículos del mismo site: encabezado con el host.
  - Con un solo artículo: línea normal.

## v0.2: detección por feed
- Nuevo `feed.go`: descubre el feed (`<link rel="alternate">` o rutas comunes), lee RSS y Atom.
- Tags por frecuencia en los últimos 50 posts (mínimo 2 posts y 10 %, máximo 4 tags).
  El vocabulario es la tabla `vocab` en `feed.go`.
- La descripción sale del `<description>` del canal del feed; respaldo: meta description.
- Tipo de site: `blog`, `forum`, `docs`, `newsletter`, `site`.
- Comandos nuevos:
  - `detect <url>`: muestra lo detectado sin guardar.
  - `add auto <urls>`: reparte cada site en la carpeta de cada tema detectado.
- Campo `locked: true` en `sites.json`: `refresh` no toca esa ficha (edición manual).
- Nombres de tema para mostrar (`Go`, `AI & ML`, `DevOps`, `JavaScript`) en el mapa `names`.

## v0.1: base
- CLI en Go, solo stdlib.
- `add <tema> <urls>`: captura título y descripción (og:title, og:description, <title>) en paralelo.
- URLs normalizadas: sin `utm_*`, `fbclid`, `gclid`, etc.; deduplicadas.
- Estructura: `topics/<tema>/sites.json` y `README.md` por tema, más `README.md` índice en la raíz.
- `refresh`, `build`, `push` (git add, commit y push).
- Workflow `.github/workflows/refresh.yml`: refresca la metadata cada lunes.
- Salida determinista (sin fechas) para no generar commits vacíos.

## Pendiente / límites conocidos
- La detección es heurística: revisa con `detect` y usa `locked` para fijar fichas.
- Los temas dependen de la tabla `vocab`; lo que no esté ahí no se detecta.
- Sufijos en títulos de artículos (` | Proton`) no se recortan.
- Una página de blog cuyo path sea un slug (`/mi-blog-tech`) puede detectarse como artículo.
- No probé los feeds de Netflix ni de otros sites; solo el de Proton lo viste funcionar.