package doppler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// Project is one Doppler project.
type Project struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

// Environment is one environment within a project (dev, stg, prd, …).
type Environment struct {
	ID              string `json:"id"`
	Slug            string `json:"slug"`
	Name            string `json:"name"`
	Project         string `json:"project"`
	PersonalConfigs bool   `json:"personal_configs"`
}

// Config is one config (a branch of an environment).
type Config struct {
	Name        string     `json:"name"`
	Root        bool       `json:"root"`
	Locked      bool       `json:"locked"`
	Inheritable bool       `json:"inheritable"`
	Inheriting  bool       `json:"inheriting"`
	Inherits    []Inherit  `json:"inherits"`
	Environment string     `json:"environment"`
	Project     string     `json:"project"`
	CreatedAt   time.Time  `json:"created_at"`
	LastFetchAt *time.Time `json:"last_fetch_at"`
}

// Inherit names a config another config inherits secrets from.
type Inherit struct {
	Project string `json:"project"`
	Config  string `json:"config"`
}

// Secret is one secret's raw and computed forms.
//
// raw is what is stored; computed is raw after ${VAR} substitution, so the two
// differ exactly for references — which is the thing worth showing side by
// side when the value is not what someone expected.
type Secret struct {
	Name               string `json:"-"`
	Raw                string `json:"raw"`
	Computed           string `json:"computed"`
	Note               string `json:"note"`
	RawVisibility      string `json:"rawVisibility"`
	ComputedVisibility string `json:"computedVisibility"`
	RawValueType       Type   `json:"rawValueType"`
	ComputedValueType  Type   `json:"computedValueType"`
}

// Type is a secret's declared value type.
type Type struct {
	Type string `json:"type"`
}

// Referenced reports whether the stored value contains a reference that the
// computed value resolved, i.e. whether the two forms differ meaningfully.
func (s Secret) Referenced() bool { return s.Raw != s.Computed }

// Restricted reports whether the API withheld the value. Doppler returns an
// empty string for a restricted secret rather than an error, so without this
// check a restricted secret is indistinguishable from an empty one.
func (s Secret) Restricted() bool { return s.ComputedVisibility == "restricted" }

// ---- listing --------------------------------------------------------------

// Page is one page of a listing together with the ETag that revalidates it.
//
// Pages are cached individually rather than as one flat list because that is
// the granularity the API revalidates at: a workplace of several hundred
// projects is a handful of pages, and adding one project invalidates only the
// last of them.
type Page[T any] struct {
	ETag  string `json:"etag"`
	Items []T    `json:"items"`
}

const perPage = 100

// runawayPages guards against an API that stops honoring per_page; 50 pages is
// far past any real workplace.
const runawayPages = 50

// Projects lists every project, reusing prev where the server says nothing
// changed.
//
// Each cached page is revalidated with If-None-Match. A 304 costs no body and
// the cached items are reused verbatim; only pages that actually changed are
// transferred. Over several hundred projects that turns a no-op refresh from
// tens of KB of JSON into a few sets of headers.
//
// The cached pages are revalidated concurrently, because with no bodies to
// transfer the cost is entirely round-trip latency — four sequential 304s take
// four times as long as four parallel ones and move exactly as much data.
// Pages past the cached end are still walked sequentially: their count is not
// known ahead of time.
func (c *Client) Projects(ctx context.Context, prev []Page[Project]) ([]Page[Project], error) {
	out, next, err := c.revalidatePages(ctx, prev)
	if err != nil {
		return nil, err
	}
	if next == 0 {
		return out, nil
	}

	for page := next; page <= runawayPages; page++ {
		items, etag, _, err := c.projectPage(ctx, page, "")
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			break
		}
		out = append(out, Page[Project]{ETag: etag, Items: items})
		if len(items) < perPage {
			break
		}
	}
	return out, nil
}

// revalidatePages checks every cached page at once and returns the pages that
// survived plus the first page number still to be walked (0 when the listing
// is known to have ended).
func (c *Client) revalidatePages(ctx context.Context, prev []Page[Project]) ([]Page[Project], int, error) {
	if len(prev) == 0 {
		return nil, 1, nil
	}

	type result struct {
		page Page[Project]
		err  error
	}
	results := make([]result, len(prev))
	var wg sync.WaitGroup
	for i := range prev {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			items, etag, notModified, err := c.projectPage(ctx, i+1, prev[i].ETag)
			switch {
			case err != nil:
				results[i] = result{err: err}
			case notModified:
				results[i] = result{page: prev[i]}
			default:
				results[i] = result{page: Page[Project]{ETag: etag, Items: items}}
			}
		}(i)
	}
	wg.Wait()

	out := make([]Page[Project], 0, len(prev))
	for _, r := range results {
		if r.err != nil {
			return nil, 0, r.err
		}
		if len(r.page.Items) == 0 {
			// The listing shrank past this page; everything after it is gone
			// too, and the walk is over.
			return out, 0, nil
		}
		out = append(out, r.page)
		if len(r.page.Items) < perPage {
			// A short page ends the listing exactly as it did when cached, so
			// no probe request is needed to confirm it.
			return out, 0, nil
		}
	}
	// Every cached page was full: the listing may have grown past them.
	return out, len(out) + 1, nil
}

// projectPage fetches or revalidates one page.
func (c *Client) projectPage(ctx context.Context, page int, etag string) ([]Project, string, bool, error) {
	q := url.Values{
		"per_page": {strconv.Itoa(perPage)},
		"page":     {strconv.Itoa(page)},
	}
	res, err := c.get(ctx, "/v3/projects", q, etag)
	if err != nil {
		return nil, "", false, err
	}
	if res.NotModified {
		return nil, res.ETag, true, nil
	}
	var env struct {
		Projects []Project `json:"projects"`
	}
	if err := json.Unmarshal(res.Body, &env); err != nil {
		return nil, "", false, fmt.Errorf("parse projects: %w", err)
	}
	return env.Projects, res.ETag, false, nil
}

// Flatten concatenates paged items in order.
func Flatten[T any](pages []Page[T]) []T {
	n := 0
	for _, p := range pages {
		n += len(p.Items)
	}
	out := make([]T, 0, n)
	for _, p := range pages {
		out = append(out, p.Items...)
	}
	return out
}

// Environments lists a project's environments.
func (c *Client) Environments(ctx context.Context, project, etag string) ([]Environment, string, bool, error) {
	res, err := c.get(ctx, "/v3/environments", url.Values{"project": {project}}, etag)
	if err != nil {
		return nil, "", false, err
	}
	if res.NotModified {
		return nil, res.ETag, true, nil
	}
	var env struct {
		Environments []Environment `json:"environments"`
	}
	if err := json.Unmarshal(res.Body, &env); err != nil {
		return nil, "", false, fmt.Errorf("parse environments: %w", err)
	}
	return env.Environments, res.ETag, false, nil
}

// Configs lists a project's configs across all environments.
func (c *Client) Configs(ctx context.Context, project, etag string) ([]Config, string, bool, error) {
	q := url.Values{"project": {project}, "per_page": {strconv.Itoa(perPage)}}
	res, err := c.get(ctx, "/v3/configs", q, etag)
	if err != nil {
		return nil, "", false, err
	}
	if res.NotModified {
		return nil, res.ETag, true, nil
	}
	var env struct {
		Configs []Config `json:"configs"`
	}
	if err := json.Unmarshal(res.Body, &env); err != nil {
		return nil, "", false, fmt.Errorf("parse configs: %w", err)
	}
	return env.Configs, res.ETag, false, nil
}

// SecretNames lists a config's secret names without transferring any values.
//
// This is what the browser renders, and it keeps values off the wire and out
// of the cache until someone explicitly asks to reveal one.
func (c *Client) SecretNames(ctx context.Context, project, config, etag string) ([]string, string, bool, error) {
	q := url.Values{"project": {project}, "config": {config}}
	res, err := c.get(ctx, "/v3/configs/config/secrets/names", q, etag)
	if err != nil {
		return nil, "", false, err
	}
	if res.NotModified {
		return nil, res.ETag, true, nil
	}
	var env struct {
		Names []string `json:"names"`
	}
	if err := json.Unmarshal(res.Body, &env); err != nil {
		return nil, "", false, fmt.Errorf("parse secret names: %w", err)
	}
	return env.Names, res.ETag, false, nil
}

// Secrets fetches a config's secrets with values. Never cached to disk.
func (c *Client) Secrets(ctx context.Context, project, config string) ([]Secret, error) {
	q := url.Values{"project": {project}, "config": {config}}
	res, err := c.get(ctx, "/v3/configs/config/secrets", q, "")
	if err != nil {
		return nil, err
	}
	var env struct {
		Secrets map[string]Secret `json:"secrets"`
	}
	if err := json.Unmarshal(res.Body, &env); err != nil {
		return nil, fmt.Errorf("parse secrets: %w", err)
	}
	out := make([]Secret, 0, len(env.Secrets))
	for name, s := range env.Secrets {
		s.Name = name
		out = append(out, s)
	}
	return out, nil
}

// Workplace returns the workplace this token belongs to, used to label the UI
// and to detect that a cache belongs to a different workplace.
func (c *Client) Workplace(ctx context.Context) (id, name string, err error) {
	res, err := c.get(ctx, "/v3/workplace", nil, "")
	if err != nil {
		return "", "", err
	}
	var env struct {
		Workplace struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"workplace"`
	}
	if err := json.Unmarshal(res.Body, &env); err != nil {
		return "", "", fmt.Errorf("parse workplace: %w", err)
	}
	return env.Workplace.ID, env.Workplace.Name, nil
}
